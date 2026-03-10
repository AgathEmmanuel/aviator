/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package latency

import (
	"sort"
	"time"
)

// PodRanking holds a pod's identity and latency stats for selection.
type PodRanking struct {
	PodName string
	PodIP   string
	Stats   Stats
}

// RankPods sorts pods by P99 latency (lowest first).
func RankPods(pods []PodRanking) []PodRanking {
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].Stats.P99 < pods[j].Stats.P99
	})
	return pods
}

// SelectTopN returns the N fastest pods.
func SelectTopN(ranked []PodRanking, n int) []PodRanking {
	if n <= 0 || n >= len(ranked) {
		return ranked
	}
	return ranked[:n]
}

// SelectTopPercent returns the top X% of pods (minimum 1).
func SelectTopPercent(ranked []PodRanking, percent int) []PodRanking {
	if percent <= 0 {
		percent = 100
	}
	n := (len(ranked) * percent) / 100
	if n < 1 {
		n = 1
	}
	if n > len(ranked) {
		n = len(ranked)
	}
	return ranked[:n]
}

// SelectByThreshold returns pods with P99 below the threshold.
// Always returns at least 1 pod (the fastest) even if all exceed the threshold.
func SelectByThreshold(ranked []PodRanking, threshold time.Duration) []PodRanking {
	var selected []PodRanking
	for _, p := range ranked {
		if p.Stats.P99 <= threshold {
			selected = append(selected, p)
		}
	}
	if len(selected) == 0 && len(ranked) > 0 {
		selected = append(selected, ranked[0])
	}
	return selected
}

// ComputeFleetP99 computes the P99 across all pods in the fleet.
func ComputeFleetP99(pods []PodRanking) time.Duration {
	if len(pods) == 0 {
		return 0
	}
	latencies := make([]time.Duration, len(pods))
	for i, p := range pods {
		latencies[i] = p.Stats.P99
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	idx := (99 * len(latencies)) / 100
	if idx >= len(latencies) {
		idx = len(latencies) - 1
	}
	return latencies[idx]
}

// ComputeFleetAverage computes the average P99 across all pods.
func ComputeFleetAverage(pods []PodRanking) time.Duration {
	if len(pods) == 0 {
		return 0
	}
	var total time.Duration
	for _, p := range pods {
		total += p.Stats.P99
	}
	return total / time.Duration(len(pods))
}

// DampeningState tracks whether endpoint updates should be suppressed.
type DampeningState struct {
	previousSelected []string
	violationCount   int
}

// NewDampeningState creates a new dampening tracker.
func NewDampeningState() *DampeningState {
	return &DampeningState{}
}

// ShouldUpdate returns true if the new pod set differs enough from the previous
// set to warrant an endpoint update.
func (d *DampeningState) ShouldUpdate(newSelected []string, thresholdPercent int, requiredConsecutive int) bool {
	if len(d.previousSelected) == 0 {
		d.previousSelected = newSelected
		d.violationCount = 0
		return true
	}

	changePercent := d.computeChangePercent(newSelected)
	if changePercent >= float64(thresholdPercent) {
		d.violationCount++
	} else {
		d.violationCount = 0
	}

	if d.violationCount >= requiredConsecutive {
		d.previousSelected = newSelected
		d.violationCount = 0
		return true
	}

	return false
}

func (d *DampeningState) computeChangePercent(newSelected []string) float64 {
	if len(d.previousSelected) == 0 {
		return 100
	}

	prevSet := make(map[string]bool, len(d.previousSelected))
	for _, ip := range d.previousSelected {
		prevSet[ip] = true
	}

	changed := 0
	for _, ip := range newSelected {
		if !prevSet[ip] {
			changed++
		}
	}

	total := len(d.previousSelected)
	if len(newSelected) > total {
		total = len(newSelected)
	}

	return (float64(changed) / float64(total)) * 100
}
