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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// AgentResponse is the JSON response from an eBPF agent's /latencies endpoint.
type AgentResponse struct {
	PodLatencies map[string]AgentPodStats `json:"podLatencies"`
	NodeName     string                   `json:"nodeName"`
}

// AgentPodStats is a single pod's stats as reported by the agent.
type AgentPodStats struct {
	P50Us       int64 `json:"p50Us"`
	P99Us       int64 `json:"p99Us"`
	SampleCount int64 `json:"sampleCount"`
}

// EBPFSource reads latency data from eBPF agents running as a DaemonSet.
type EBPFSource struct {
	log        logr.Logger
	httpClient *http.Client

	// agentEndpoints is a list of agent HTTP addresses (host:port).
	mu             sync.RWMutex
	agentEndpoints []string
}

// NewEBPFSource creates a new eBPF-backed latency source.
func NewEBPFSource(log logr.Logger) *EBPFSource {
	return &EBPFSource{
		log: log.WithName("ebpf-source"),
		httpClient: &http.Client{
			Timeout: 3 * time.Second,
		},
	}
}

// UpdateAgentEndpoints updates the list of agent addresses to poll.
func (s *EBPFSource) UpdateAgentEndpoints(endpoints []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentEndpoints = endpoints
}

func (s *EBPFSource) Name() string { return "ebpf" }

func (s *EBPFSource) Ready(ctx context.Context) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.agentEndpoints) > 0
}

// GetLatencies aggregates latency data from all known eBPF agents.
func (s *EBPFSource) GetLatencies(ctx context.Context, podIPs []string) (map[string]Stats, error) {
	s.mu.RLock()
	endpoints := make([]string, len(s.agentEndpoints))
	copy(endpoints, s.agentEndpoints)
	s.mu.RUnlock()

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no eBPF agent endpoints available")
	}

	type result struct {
		stats map[string]Stats
		err   error
	}

	results := make(chan result, len(endpoints))
	for _, ep := range endpoints {
		go func(endpoint string) {
			stats, err := s.fetchFromAgent(ctx, endpoint, podIPs)
			results <- result{stats: stats, err: err}
		}(ep)
	}

	aggregated := make(map[string]Stats)
	var lastErr error

	for range endpoints {
		r := <-results
		if r.err != nil {
			s.log.V(1).Info("failed to fetch from agent", "error", r.err)
			lastErr = r.err
			continue
		}
		for ip, stat := range r.stats {
			existing, ok := aggregated[ip]
			if !ok || stat.SampleCount > existing.SampleCount {
				aggregated[ip] = stat
			}
		}
	}

	if len(aggregated) == 0 && lastErr != nil {
		return nil, fmt.Errorf("all agent fetches failed, last error: %w", lastErr)
	}

	return aggregated, nil
}

func (s *EBPFSource) fetchFromAgent(ctx context.Context, endpoint string, podIPs []string) (map[string]Stats, error) {
	url := fmt.Sprintf("http://%s/latencies", endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching from agent %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent %s returned status %d", endpoint, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var agentResp AgentResponse
	if err := json.Unmarshal(body, &agentResp); err != nil {
		return nil, fmt.Errorf("unmarshaling agent response: %w", err)
	}

	// Filter to requested pod IPs and convert to Stats.
	podIPSet := make(map[string]bool, len(podIPs))
	for _, ip := range podIPs {
		podIPSet[ip] = true
	}

	stats := make(map[string]Stats)
	for ip, agentStat := range agentResp.PodLatencies {
		if len(podIPs) > 0 && !podIPSet[ip] {
			continue
		}
		stats[ip] = Stats{
			P50:         time.Duration(agentStat.P50Us) * time.Microsecond,
			P99:         time.Duration(agentStat.P99Us) * time.Microsecond,
			SampleCount: agentStat.SampleCount,
			LastUpdated: time.Now(),
		}
	}

	return stats, nil
}
