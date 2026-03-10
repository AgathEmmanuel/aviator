// SPDX-License-Identifier: GPL-2.0
// Copyright 2025 Aviator Authors.
//
// eBPF program to measure TCP round-trip latency at the kernel level.
// Attaches to TCP socket operations to capture real traffic latency
// without generating synthetic probes.
//
// Requires kernel 5.8+ with BTF support.

// vmlinux.h is generated at build time via:
//   bpftool btf dump file /sys/kernel/btf/vmlinux format c > vmlinux.h
// See headers/README.md for details.
#include "headers/vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>

#define MAX_ENTRIES 65536

// Flow key identifies a TCP connection.
struct flow_key {
    __u32 src_ip;
    __u32 dst_ip;
    __u16 src_port;
    __u16 dst_port;
};

// Latency sample sent to userspace via ring buffer.
struct latency_event {
    __u32 src_ip;
    __u32 dst_ip;
    __u16 src_port;
    __u16 dst_port;
    __u64 rtt_ns;       // Round-trip time in nanoseconds.
    __u64 timestamp_ns;  // When the measurement was taken.
};

// Tracks the timestamp of outgoing TCP segments.
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, MAX_ENTRIES);
    __type(key, struct flow_key);
    __type(value, __u64); // Timestamp in nanoseconds.
} tcp_send_timestamps SEC(".maps");

// Ring buffer for sending latency events to userspace.
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20); // 1MB ring buffer.
} latency_events SEC(".maps");

// Per-destination-IP aggregated latency (updated in-kernel for fast reads).
struct latency_agg {
    __u64 total_rtt_ns;
    __u64 sample_count;
    __u64 max_rtt_ns;
    __u64 min_rtt_ns;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, MAX_ENTRIES);
    __type(key, __u32);               // Destination IP.
    __type(value, struct latency_agg);
} per_ip_latency SEC(".maps");

static __always_inline void extract_flow(struct sock *sk, struct flow_key *key) {
    BPF_CORE_READ_INTO(&key->src_ip, sk, __sk_common.skc_rcv_saddr);
    BPF_CORE_READ_INTO(&key->dst_ip, sk, __sk_common.skc_daddr);
    BPF_CORE_READ_INTO(&key->src_port, sk, __sk_common.skc_num);
    __u16 dst_port;
    BPF_CORE_READ_INTO(&dst_port, sk, __sk_common.skc_dport);
    key->dst_port = __builtin_bswap16(dst_port);
}

// Hook: tcp_sendmsg — record timestamp when data is sent.
SEC("kprobe/tcp_sendmsg")
int BPF_KPROBE(kprobe_tcp_sendmsg, struct sock *sk) {
    struct flow_key key = {};
    extract_flow(sk, &key);

    __u64 ts = bpf_ktime_get_ns();
    bpf_map_update_elem(&tcp_send_timestamps, &key, &ts, BPF_ANY);

    return 0;
}

// Hook: tcp_rcv_established — compute RTT when ACK is received.
SEC("kprobe/tcp_rcv_established")
int BPF_KPROBE(kprobe_tcp_rcv_established, struct sock *sk) {
    struct flow_key key = {};
    extract_flow(sk, &key);

    // Look up the send timestamp.
    __u64 *send_ts = bpf_map_lookup_elem(&tcp_send_timestamps, &key);
    if (!send_ts) {
        return 0;
    }

    __u64 now = bpf_ktime_get_ns();
    __u64 rtt_ns = now - *send_ts;

    // Clean up the timestamp entry.
    bpf_map_delete_elem(&tcp_send_timestamps, &key);

    // Ignore obviously invalid RTTs (> 30 seconds).
    if (rtt_ns > 30000000000ULL) {
        return 0;
    }

    // Send event to userspace via ring buffer.
    struct latency_event *evt;
    evt = bpf_ringbuf_reserve(&latency_events, sizeof(*evt), 0);
    if (evt) {
        evt->src_ip = key.src_ip;
        evt->dst_ip = key.dst_ip;
        evt->src_port = key.src_port;
        evt->dst_port = key.dst_port;
        evt->rtt_ns = rtt_ns;
        evt->timestamp_ns = now;
        bpf_ringbuf_submit(evt, 0);
    }

    // Update per-IP aggregation.
    struct latency_agg *agg = bpf_map_lookup_elem(&per_ip_latency, &key.dst_ip);
    if (agg) {
        __sync_fetch_and_add(&agg->total_rtt_ns, rtt_ns);
        __sync_fetch_and_add(&agg->sample_count, 1);
        if (rtt_ns > agg->max_rtt_ns)
            agg->max_rtt_ns = rtt_ns;
        if (rtt_ns < agg->min_rtt_ns || agg->min_rtt_ns == 0)
            agg->min_rtt_ns = rtt_ns;
    } else {
        struct latency_agg new_agg = {
            .total_rtt_ns = rtt_ns,
            .sample_count = 1,
            .max_rtt_ns = rtt_ns,
            .min_rtt_ns = rtt_ns,
        };
        bpf_map_update_elem(&per_ip_latency, &key.dst_ip, &new_agg, BPF_NOEXIST);
    }

    return 0;
}

char LICENSE[] SEC("license") = "GPL";
