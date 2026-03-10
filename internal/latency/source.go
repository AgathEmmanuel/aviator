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
	"time"
)

// Stats holds per-pod latency statistics.
type Stats struct {
	P50         time.Duration
	P99         time.Duration
	SampleCount int64
	LastUpdated time.Time
}

// Source is the interface that latency measurement backends must implement.
type Source interface {
	// GetLatencies returns latency statistics for a set of pod IPs.
	// The returned map is keyed by pod IP.
	GetLatencies(ctx context.Context, podIPs []string) (map[string]Stats, error)

	// Name returns a human-readable name for the latency source.
	Name() string

	// Ready returns true if the source is ready to serve latency data.
	Ready(ctx context.Context) bool
}
