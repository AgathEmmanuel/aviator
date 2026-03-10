/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package circuitbreaker

import (
	"sync"
	"time"
)

// State represents the circuit breaker state for a single pod.
type State int

const (
	StateClosed   State = iota // Healthy — traffic flows normally.
	StateOpen                  // Ejected — no traffic sent to this pod.
	StateHalfOpen              // Recovery probe — testing if pod is healthy again.
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// PodState tracks the circuit breaker state for a single pod.
type PodState struct {
	State               State
	ViolationCount      int32
	LastViolationTime   time.Time
	EjectedAt           time.Time
	LastRecoveryProbeAt time.Time
}

// Breaker manages circuit breaker state for a set of pods.
type Breaker struct {
	mu                    sync.RWMutex
	pods                  map[string]*PodState // keyed by pod IP
	p99Threshold          time.Duration
	consecutiveViolations int32
	recoveryInterval      time.Duration
}

// Config holds circuit breaker parameters.
type Config struct {
	P99Threshold          time.Duration
	ConsecutiveViolations int32
	RecoveryInterval      time.Duration
}

// New creates a new circuit breaker with the given configuration.
func New(cfg Config) *Breaker {
	return &Breaker{
		pods:                  make(map[string]*PodState),
		p99Threshold:          cfg.P99Threshold,
		consecutiveViolations: cfg.ConsecutiveViolations,
		recoveryInterval:      cfg.RecoveryInterval,
	}
}

// RecordLatency records a latency observation for a pod and transitions state.
func (b *Breaker) RecordLatency(podIP string, p99 time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ps, ok := b.pods[podIP]
	if !ok {
		ps = &PodState{State: StateClosed}
		b.pods[podIP] = ps
	}

	switch ps.State {
	case StateClosed:
		if p99 > b.p99Threshold {
			ps.ViolationCount++
			ps.LastViolationTime = time.Now()
			if ps.ViolationCount >= b.consecutiveViolations {
				ps.State = StateOpen
				ps.EjectedAt = time.Now()
			}
		} else {
			ps.ViolationCount = 0
		}

	case StateOpen:
		// Pod is ejected. Check if recovery interval has elapsed.
		if time.Since(ps.EjectedAt) >= b.recoveryInterval {
			ps.State = StateHalfOpen
			ps.LastRecoveryProbeAt = time.Now()
		}

	case StateHalfOpen:
		// Pod is being probed for recovery.
		if p99 <= b.p99Threshold {
			ps.State = StateClosed
			ps.ViolationCount = 0
		} else {
			ps.State = StateOpen
			ps.EjectedAt = time.Now()
		}
	}
}

// IsEjected returns true if the pod should not receive traffic.
func (b *Breaker) IsEjected(podIP string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	ps, ok := b.pods[podIP]
	if !ok {
		return false
	}
	return ps.State == StateOpen
}

// GetEjectedPods returns a list of all currently ejected pod IPs.
func (b *Breaker) GetEjectedPods() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var ejected []string
	for ip, ps := range b.pods {
		if ps.State == StateOpen {
			ejected = append(ejected, ip)
		}
	}
	return ejected
}

// GetState returns the current state for a pod.
func (b *Breaker) GetState(podIP string) (PodState, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	ps, ok := b.pods[podIP]
	if !ok {
		return PodState{}, false
	}
	return *ps, true
}

// CheckRecovery transitions eligible open pods to half-open for re-probing.
func (b *Breaker) CheckRecovery() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	var transitioned []string
	for ip, ps := range b.pods {
		if ps.State == StateOpen && time.Since(ps.EjectedAt) >= b.recoveryInterval {
			ps.State = StateHalfOpen
			ps.LastRecoveryProbeAt = time.Now()
			transitioned = append(transitioned, ip)
		}
	}
	return transitioned
}

// RemovePod removes tracking for a pod that no longer exists.
func (b *Breaker) RemovePod(podIP string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pods, podIP)
}

// Reset clears all state.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pods = make(map[string]*PodState)
}
