/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package circuitbreaker

import (
	"testing"
	"time"
)

func newTestBreaker() *Breaker {
	return New(Config{
		P99Threshold:          100 * time.Millisecond,
		ConsecutiveViolations: 3,
		RecoveryInterval:      1 * time.Second,
	})
}

func TestNewPodStartsClosed(t *testing.T) {
	b := newTestBreaker()
	b.RecordLatency("10.0.0.1", 50*time.Millisecond)

	if b.IsEjected("10.0.0.1") {
		t.Error("new pod with good latency should not be ejected")
	}
}

func TestEjectionAfterConsecutiveViolations(t *testing.T) {
	b := newTestBreaker()

	// 3 consecutive violations should eject.
	b.RecordLatency("10.0.0.1", 200*time.Millisecond)
	b.RecordLatency("10.0.0.1", 200*time.Millisecond)
	b.RecordLatency("10.0.0.1", 200*time.Millisecond)

	if !b.IsEjected("10.0.0.1") {
		t.Error("pod should be ejected after 3 consecutive violations")
	}
}

func TestViolationCountResets(t *testing.T) {
	b := newTestBreaker()

	// 2 violations, then a good reading.
	b.RecordLatency("10.0.0.1", 200*time.Millisecond)
	b.RecordLatency("10.0.0.1", 200*time.Millisecond)
	b.RecordLatency("10.0.0.1", 50*time.Millisecond) // resets count

	if b.IsEjected("10.0.0.1") {
		t.Error("violation count should reset after good reading")
	}

	state, ok := b.GetState("10.0.0.1")
	if !ok {
		t.Fatal("state should exist")
	}
	if state.ViolationCount != 0 {
		t.Errorf("expected violation count 0, got %d", state.ViolationCount)
	}
}

func TestGetEjectedPods(t *testing.T) {
	b := newTestBreaker()

	// Eject pod-1.
	for i := 0; i < 3; i++ {
		b.RecordLatency("10.0.0.1", 200*time.Millisecond)
	}
	// pod-2 is healthy.
	b.RecordLatency("10.0.0.2", 50*time.Millisecond)

	ejected := b.GetEjectedPods()
	if len(ejected) != 1 {
		t.Fatalf("expected 1 ejected pod, got %d", len(ejected))
	}
	if ejected[0] != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1 ejected, got %s", ejected[0])
	}
}

func TestUnknownPodIsNotEjected(t *testing.T) {
	b := newTestBreaker()
	if b.IsEjected("10.0.0.99") {
		t.Error("unknown pod should not be ejected")
	}
}

func TestRemovePod(t *testing.T) {
	b := newTestBreaker()
	b.RecordLatency("10.0.0.1", 50*time.Millisecond)
	b.RemovePod("10.0.0.1")

	_, ok := b.GetState("10.0.0.1")
	if ok {
		t.Error("removed pod should not have state")
	}
}

func TestReset(t *testing.T) {
	b := newTestBreaker()
	b.RecordLatency("10.0.0.1", 50*time.Millisecond)
	b.RecordLatency("10.0.0.2", 50*time.Millisecond)
	b.Reset()

	ejected := b.GetEjectedPods()
	if len(ejected) != 0 {
		t.Error("reset should clear all state")
	}
}
