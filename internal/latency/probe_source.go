/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package latency

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

const (
	defaultProbeTimeout  = 5 * time.Second
	unreachableLatency   = 9999 * time.Millisecond
	probeSamplesPerRound = 3
)

// ProbeSource measures latency by sending HTTP GET requests to pods.
// This is the fallback for environments without eBPF support.
type ProbeSource struct {
	log        logr.Logger
	port       int32
	httpClient *http.Client
}

// NewProbeSource creates a new HTTP-probe-backed latency source.
func NewProbeSource(log logr.Logger, port int32) *ProbeSource {
	return &ProbeSource{
		log:  log.WithName("probe-source"),
		port: port,
		httpClient: &http.Client{
			Timeout: defaultProbeTimeout,
		},
	}
}

func (s *ProbeSource) Name() string { return "probe" }

func (s *ProbeSource) Ready(_ context.Context) bool { return true }

// GetLatencies probes each pod IP and returns latency statistics.
func (s *ProbeSource) GetLatencies(ctx context.Context, podIPs []string) (map[string]Stats, error) {
	if len(podIPs) == 0 {
		return nil, nil
	}

	type result struct {
		ip   string
		stat Stats
	}

	var wg sync.WaitGroup
	results := make(chan result, len(podIPs))

	for _, ip := range podIPs {
		wg.Add(1)
		go func(podIP string) {
			defer wg.Done()
			stat := s.probePod(ctx, podIP)
			results <- result{ip: podIP, stat: stat}
		}(ip)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	stats := make(map[string]Stats, len(podIPs))
	for r := range results {
		stats[r.ip] = r.stat
	}

	return stats, nil
}

// probePod sends multiple HTTP probes and computes latency stats.
func (s *ProbeSource) probePod(ctx context.Context, podIP string) Stats {
	url := fmt.Sprintf("http://%s:%d/", podIP, s.port)
	samples := make([]time.Duration, 0, probeSamplesPerRound)

	for i := 0; i < probeSamplesPerRound; i++ {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			s.log.V(1).Info("failed to create probe request", "podIP", podIP, "error", err)
			samples = append(samples, unreachableLatency)
			continue
		}

		resp, err := s.httpClient.Do(req)
		latency := time.Since(start)

		if err != nil {
			s.log.V(1).Info("probe failed", "podIP", podIP, "error", err)
			samples = append(samples, unreachableLatency)
			continue
		}
		resp.Body.Close()
		samples = append(samples, latency)
	}

	if len(samples) == 0 {
		return Stats{
			P50:         unreachableLatency,
			P99:         unreachableLatency,
			SampleCount: 0,
			LastUpdated: time.Now(),
		}
	}

	// Sort for percentile calculation.
	sortDurations(samples)

	return Stats{
		P50:         percentile(samples, 50),
		P99:         percentile(samples, 99),
		SampleCount: int64(len(samples)),
		LastUpdated: time.Now(),
	}
}

func sortDurations(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j] < d[j-1]; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}

func percentile(sorted []time.Duration, pct int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (pct * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
