/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ebpf

import (
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestCollectorRecordAndGetStats(t *testing.T) {
	log := zap.New(zap.UseDevMode(true))
	c := NewCollector(log, 60*time.Second)

	// Simulate events from a pod at 10.0.0.1 (IP in little-endian).
	// 10.0.0.1 = 0x0100000A in little-endian uint32 = 167772161 big-endian
	// In little-endian: bytes [1, 0, 0, 10] -> uint32 = 0x0A000001 = 167772161
	// Actually we need to think about this differently.
	// uint32ToIP uses LittleEndian, so for IP 10.0.0.1:
	// bytes: [10, 0, 0, 1] -> LE uint32 = 1*2^24 + 0 + 0 + 10 = 16777226
	// Let's just test with known values.

	evt := LatencyEvent{
		DstIP: 0x0100000A, // 10.0.0.1 in network byte order stored as LE
		RTTNs: 5_000_000,  // 5ms
	}
	c.RecordEvent(evt)
	c.RecordEvent(LatencyEvent{DstIP: 0x0100000A, RTTNs: 10_000_000}) // 10ms
	c.RecordEvent(LatencyEvent{DstIP: 0x0100000A, RTTNs: 15_000_000}) // 15ms

	stats := c.GetStats()
	if len(stats) == 0 {
		t.Fatal("expected stats for at least one IP")
	}

	// Find the IP that was recorded.
	var found bool
	for _, stat := range stats {
		if stat.SampleCount == 3 {
			found = true
			if stat.P50Us <= 0 {
				t.Errorf("expected positive P50, got %d", stat.P50Us)
			}
			if stat.P99Us <= 0 {
				t.Errorf("expected positive P99, got %d", stat.P99Us)
			}
		}
	}
	if !found {
		t.Error("expected to find stats with 3 samples")
	}
}

func TestCollectorReset(t *testing.T) {
	log := zap.New(zap.UseDevMode(true))
	c := NewCollector(log, 60*time.Second)

	c.RecordEvent(LatencyEvent{DstIP: 0x0100000A, RTTNs: 5_000_000})
	c.Reset()

	stats := c.GetStats()
	if len(stats) != 0 {
		t.Errorf("expected empty stats after reset, got %d entries", len(stats))
	}
}

func TestCollectorEvictStale(t *testing.T) {
	log := zap.New(zap.UseDevMode(true))
	c := NewCollector(log, 60*time.Second)

	c.RecordEvent(LatencyEvent{DstIP: 0x0100000A, RTTNs: 5_000_000})
	c.RecordEvent(LatencyEvent{DstIP: 0x0200000A, RTTNs: 5_000_000})

	// Only keep 10.0.0.1.
	activeIPs := map[string]bool{
		uint32ToIP(0x0100000A): true,
	}
	c.EvictStale(activeIPs)

	stats := c.GetStats()
	if len(stats) != 1 {
		t.Errorf("expected 1 IP after eviction, got %d", len(stats))
	}
}

func TestParseLatencyEvent(t *testing.T) {
	data := make([]byte, 28)
	// SrcIP: 10.0.0.1
	data[0] = 10
	data[1] = 0
	data[2] = 0
	data[3] = 1
	// DstIP: 10.0.0.2
	data[4] = 10
	data[5] = 0
	data[6] = 0
	data[7] = 2
	// SrcPort: 12345 (LE)
	data[8] = 0x39
	data[9] = 0x30
	// DstPort: 80 (LE)
	data[10] = 0x50
	data[11] = 0x00
	// RTTNs: 5000000 (5ms, LE)
	data[12] = 0x40
	data[13] = 0x4B
	data[14] = 0x4C
	data[15] = 0x00
	data[16] = 0x00
	data[17] = 0x00
	data[18] = 0x00
	data[19] = 0x00
	// TimestampNs
	data[20] = 0x01
	data[21] = 0x00
	data[22] = 0x00
	data[23] = 0x00
	data[24] = 0x00
	data[25] = 0x00
	data[26] = 0x00
	data[27] = 0x00

	evt, err := ParseLatencyEvent(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.SrcPort != 12345 {
		t.Errorf("expected src port 12345, got %d", evt.SrcPort)
	}
}

func TestParseLatencyEvent_TooShort(t *testing.T) {
	_, err := ParseLatencyEvent([]byte{1, 2, 3})
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestPercentileUint64(t *testing.T) {
	samples := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	p50 := percentileUint64(samples, 50)
	if p50 < 4 || p50 > 6 {
		t.Errorf("expected P50 around 5, got %d", p50)
	}

	p99 := percentileUint64(samples, 99)
	if p99 < 9 {
		t.Errorf("expected P99 around 10, got %d", p99)
	}
}
