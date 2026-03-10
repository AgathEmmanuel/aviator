/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ebpf

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// LatencyEvent mirrors the BPF latency_event struct.
type LatencyEvent struct {
	SrcIP       uint32
	DstIP       uint32
	SrcPort     uint16
	DstPort     uint16
	RTTNs       uint64
	TimestampNs uint64
}

// PodStats holds aggregated latency statistics for a single pod IP.
type PodStats struct {
	P50Us       int64
	P99Us       int64
	SampleCount int64
	LastUpdated time.Time
}

// Collector aggregates eBPF latency events into per-pod statistics.
type Collector struct {
	mu      sync.RWMutex
	log     logr.Logger
	samples map[string][]uint64 // IP -> list of RTT samples in nanoseconds
	maxAge  time.Duration       // Max age of samples before eviction
}

// NewCollector creates a new latency event collector.
func NewCollector(log logr.Logger, maxAge time.Duration) *Collector {
	return &Collector{
		log:     log.WithName("collector"),
		samples: make(map[string][]uint64),
		maxAge:  maxAge,
	}
}

// RecordEvent processes a single latency event from the eBPF ring buffer.
func (c *Collector) RecordEvent(evt LatencyEvent) {
	ip := uint32ToIP(evt.DstIP)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.samples[ip] = append(c.samples[ip], evt.RTTNs)

	// Cap sample buffer per IP to prevent unbounded growth.
	const maxSamples = 10000
	if len(c.samples[ip]) > maxSamples {
		c.samples[ip] = c.samples[ip][len(c.samples[ip])-maxSamples:]
	}
}

// GetStats returns current latency stats for all tracked pod IPs.
func (c *Collector) GetStats() map[string]PodStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]PodStats, len(c.samples))
	for ip, samples := range c.samples {
		if len(samples) == 0 {
			continue
		}

		sorted := make([]uint64, len(samples))
		copy(sorted, samples)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

		result[ip] = PodStats{
			P50Us:       int64(percentileUint64(sorted, 50) / 1000), // ns -> us
			P99Us:       int64(percentileUint64(sorted, 99) / 1000),
			SampleCount: int64(len(samples)),
			LastUpdated: time.Now(),
		}
	}
	return result
}

// GetStatsForIPs returns stats filtered to specific pod IPs.
func (c *Collector) GetStatsForIPs(ips []string) map[string]PodStats {
	all := c.GetStats()
	if len(ips) == 0 {
		return all
	}

	filtered := make(map[string]PodStats, len(ips))
	for _, ip := range ips {
		if stat, ok := all[ip]; ok {
			filtered[ip] = stat
		}
	}
	return filtered
}

// Reset clears all collected samples.
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = make(map[string][]uint64)
}

// EvictStale removes IPs that haven't had recent samples.
// Called periodically to prevent memory leaks from terminated pods.
func (c *Collector) EvictStale(activeIPs map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for ip := range c.samples {
		if !activeIPs[ip] {
			delete(c.samples, ip)
		}
	}
}

func uint32ToIP(ip uint32) string {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, ip)
	return net.IP(b).String()
}

func percentileUint64(sorted []uint64, pct int) uint64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := float64(pct) / 100.0 * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper || upper >= len(sorted) {
		return sorted[lower]
	}
	frac := rank - float64(lower)
	return sorted[lower] + uint64(frac*float64(sorted[upper]-sorted[lower]))
}

// ParseLatencyEvent parses raw bytes from the ring buffer into a LatencyEvent.
func ParseLatencyEvent(data []byte) (LatencyEvent, error) {
	if len(data) < 28 {
		return LatencyEvent{}, fmt.Errorf("data too short: %d bytes", len(data))
	}
	return LatencyEvent{
		SrcIP:       binary.LittleEndian.Uint32(data[0:4]),
		DstIP:       binary.LittleEndian.Uint32(data[4:8]),
		SrcPort:     binary.LittleEndian.Uint16(data[8:10]),
		DstPort:     binary.LittleEndian.Uint16(data[10:12]),
		RTTNs:       binary.LittleEndian.Uint64(data[12:20]),
		TimestampNs: binary.LittleEndian.Uint64(data[20:28]),
	}, nil
}
