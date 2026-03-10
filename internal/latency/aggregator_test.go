/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package latency

import (
	"testing"
	"time"
)

func TestRankPods(t *testing.T) {
	pods := []PodRanking{
		{PodName: "pod-c", PodIP: "10.0.0.3", Stats: Stats{P99: 300 * time.Millisecond}},
		{PodName: "pod-a", PodIP: "10.0.0.1", Stats: Stats{P99: 10 * time.Millisecond}},
		{PodName: "pod-b", PodIP: "10.0.0.2", Stats: Stats{P99: 50 * time.Millisecond}},
	}

	ranked := RankPods(pods)

	if ranked[0].PodName != "pod-a" {
		t.Errorf("expected pod-a first, got %s", ranked[0].PodName)
	}
	if ranked[1].PodName != "pod-b" {
		t.Errorf("expected pod-b second, got %s", ranked[1].PodName)
	}
	if ranked[2].PodName != "pod-c" {
		t.Errorf("expected pod-c third, got %s", ranked[2].PodName)
	}
}

func TestSelectTopN(t *testing.T) {
	pods := []PodRanking{
		{PodName: "pod-a", Stats: Stats{P99: 10 * time.Millisecond}},
		{PodName: "pod-b", Stats: Stats{P99: 50 * time.Millisecond}},
		{PodName: "pod-c", Stats: Stats{P99: 300 * time.Millisecond}},
		{PodName: "pod-d", Stats: Stats{P99: 500 * time.Millisecond}},
	}

	selected := SelectTopN(pods, 2)
	if len(selected) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(selected))
	}
	if selected[0].PodName != "pod-a" || selected[1].PodName != "pod-b" {
		t.Errorf("wrong pods selected: %v", selected)
	}
}

func TestSelectTopN_MoreThanAvailable(t *testing.T) {
	pods := []PodRanking{
		{PodName: "pod-a"},
	}
	selected := SelectTopN(pods, 5)
	if len(selected) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(selected))
	}
}

func TestSelectTopPercent(t *testing.T) {
	pods := make([]PodRanking, 10)
	for i := range pods {
		pods[i] = PodRanking{
			PodName: "pod",
			Stats:   Stats{P99: time.Duration(i+1) * time.Millisecond},
		}
	}

	selected := SelectTopPercent(pods, 30)
	if len(selected) != 3 {
		t.Fatalf("expected 3 pods (30%% of 10), got %d", len(selected))
	}
}

func TestSelectTopPercent_MinimumOne(t *testing.T) {
	pods := []PodRanking{
		{PodName: "pod-a", Stats: Stats{P99: 10 * time.Millisecond}},
		{PodName: "pod-b", Stats: Stats{P99: 50 * time.Millisecond}},
	}

	selected := SelectTopPercent(pods, 1) // 1% of 2 = 0, should clamp to 1
	if len(selected) != 1 {
		t.Fatalf("expected at least 1 pod, got %d", len(selected))
	}
}

func TestSelectByThreshold(t *testing.T) {
	pods := []PodRanking{
		{PodName: "pod-a", Stats: Stats{P99: 10 * time.Millisecond}},
		{PodName: "pod-b", Stats: Stats{P99: 50 * time.Millisecond}},
		{PodName: "pod-c", Stats: Stats{P99: 200 * time.Millisecond}},
	}

	selected := SelectByThreshold(pods, 100*time.Millisecond)
	if len(selected) != 2 {
		t.Fatalf("expected 2 pods below 100ms, got %d", len(selected))
	}
}

func TestSelectByThreshold_AllExceed(t *testing.T) {
	pods := []PodRanking{
		{PodName: "pod-a", Stats: Stats{P99: 500 * time.Millisecond}},
		{PodName: "pod-b", Stats: Stats{P99: 900 * time.Millisecond}},
	}

	selected := SelectByThreshold(pods, 100*time.Millisecond)
	if len(selected) != 1 {
		t.Fatalf("expected 1 fallback pod, got %d", len(selected))
	}
	if selected[0].PodName != "pod-a" {
		t.Errorf("expected fastest pod as fallback, got %s", selected[0].PodName)
	}
}

func TestComputeFleetP99(t *testing.T) {
	pods := []PodRanking{
		{Stats: Stats{P99: 10 * time.Millisecond}},
		{Stats: Stats{P99: 20 * time.Millisecond}},
		{Stats: Stats{P99: 30 * time.Millisecond}},
		{Stats: Stats{P99: 500 * time.Millisecond}},
	}

	p99 := ComputeFleetP99(pods)
	if p99 != 500*time.Millisecond {
		t.Errorf("expected 500ms fleet P99, got %v", p99)
	}
}

func TestComputeFleetAverage(t *testing.T) {
	pods := []PodRanking{
		{Stats: Stats{P99: 10 * time.Millisecond}},
		{Stats: Stats{P99: 20 * time.Millisecond}},
		{Stats: Stats{P99: 30 * time.Millisecond}},
	}

	avg := ComputeFleetAverage(pods)
	if avg != 20*time.Millisecond {
		t.Errorf("expected 20ms average, got %v", avg)
	}
}

func TestDampeningState_FirstUpdate(t *testing.T) {
	d := NewDampeningState()
	result := d.ShouldUpdate([]string{"10.0.0.1", "10.0.0.2"}, 20, 3)
	if !result {
		t.Error("first update should always proceed")
	}
}

func TestDampeningState_SuppressSmallChange(t *testing.T) {
	d := NewDampeningState()
	d.ShouldUpdate([]string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, 20, 3)

	// Change 1 out of 3 = 33%, but needs 3 consecutive intervals.
	result := d.ShouldUpdate([]string{"10.0.0.1", "10.0.0.2", "10.0.0.4"}, 20, 3)
	if result {
		t.Error("should suppress after only 1 interval exceeding threshold")
	}
}

func TestDampeningState_AllowAfterConsecutive(t *testing.T) {
	d := NewDampeningState()
	// First call always applies.
	d.ShouldUpdate([]string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, 20, 2)

	// Second call: 66% change, violation=1, suppressed.
	r1 := d.ShouldUpdate([]string{"10.0.0.1", "10.0.0.4", "10.0.0.5"}, 20, 2)
	if r1 {
		t.Error("should suppress after only 1 interval")
	}

	// Third call: same change, violation=2 >= required 2 -> should apply.
	r2 := d.ShouldUpdate([]string{"10.0.0.1", "10.0.0.4", "10.0.0.5"}, 20, 2)
	if !r2 {
		t.Error("should apply after 2 consecutive intervals exceeding threshold")
	}
}
