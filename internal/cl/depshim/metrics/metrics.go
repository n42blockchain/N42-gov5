// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Metrics unit for the metrics package.
// Defines the Counter, Gauge, Histogram, and Summary types.
// Provides constructors NewCounter, NewGauge, NewHistogram, and NewSummary.
// Exports helpers such as Inc, Add, Get, and Set.
// Metric registration shims.

//go:build n42el

// Package metrics is a no-op stub of erigon's diagnostics/metrics. Caplin
// uses Counter / Gauge / GetOrCreate* / NewHistTimer for observability —
// none of which are required for correctness. We provide trivial
// implementations that record nothing so the cl/ tree compiles and runs
// without pulling in a Prometheus or VictoriaMetrics dependency.
//
// When N42 wires Caplin metrics into its existing Prometheus pipeline, this
// package can be replaced with thin adapters around N42's own metrics
// registry. Until then, the no-op cost is one allocation per metric at
// startup and zero work per increment.
package metrics

import "time"

// Counter is a monotonically increasing integer metric.
type Counter struct{}

// Counter / Gauge are intentionally value-typed (empty structs). cl/ files
// declare fields as `metrics.Counter` (no pointer) and assign from
// `metrics.GetOrCreateCounter(...)` — both must agree, so the constructors
// return values, not pointers. All methods are defined on value receivers
// and do nothing, which is safe because the stub holds no state.

// Inc adds one to the counter.
func (Counter) Inc() {}

// Add adds n to the counter.
func (Counter) Add(_ float64) {}

// Get returns the current value (always 0 for the stub).
func (Counter) Get() uint64 { return 0 }

// Set sets the value (no-op for the stub).
func (Counter) Set(_ uint64) {}

// Gauge holds an arbitrary float64 value.
type Gauge struct{}

// Set replaces the gauge value.
func (Gauge) Set(_ float64) {}

// Inc adds one.
func (Gauge) Inc() {}

// Dec subtracts one.
func (Gauge) Dec() {}

// Add adds the given value.
func (Gauge) Add(_ float64) {}

// Get returns the current value (always 0 for the stub).
func (Gauge) Get() float64 { return 0 }

// GetValueUint64 returns the current value as uint64 (always 0 for the stub).
func (Gauge) GetValueUint64() uint64 { return 0 }

// Histogram records distributions of observed values.
type Histogram struct{}

// Observe records a sample value.
func (Histogram) Observe(_ float64) {}

// UpdateDuration records the elapsed time since startTime.
func (Histogram) UpdateDuration(_ time.Time) {}

// Summary records distributions with quantile estimates.
type Summary struct{}

// Observe records a sample value.
func (Summary) Observe(_ float64) {}

// UpdateDuration records the elapsed time since startTime.
func (Summary) UpdateDuration(_ time.Time) {}

// HistTimer is a timer convenience wrapper used by cl/monitor.
type HistTimer struct{}

// PutSince records the elapsed time since startTime.
func (HistTimer) PutSince() {}

// UpdateDuration records the elapsed time since startTime.
func (HistTimer) UpdateDuration(_ time.Time) {}

// Observe records a sample value (used by phase1/core/state ssz timing).
func (HistTimer) Observe(_ float64) {}

// Constructors. They all return a fresh, no-op stub instance regardless of
// the metric name.

// NewCounter creates a new Counter.
func NewCounter(_ string) Counter { return Counter{} }

// NewGauge creates a new Gauge.
func NewGauge(_ string) Gauge { return Gauge{} }

// NewHistogram creates a new Histogram.
func NewHistogram(_ string, _ []float64) Histogram { return Histogram{} }

// NewSummary creates a new Summary.
func NewSummary(_ string) Summary { return Summary{} }

// NewHistTimer creates a new HistTimer.
func NewHistTimer(_ string) HistTimer { return HistTimer{} }

// GetOrCreateCounter is the get-or-create variant.
func GetOrCreateCounter(_ string) Counter { return Counter{} }

// GetOrCreateGauge is the get-or-create variant.
func GetOrCreateGauge(_ string) Gauge { return Gauge{} }

// GetOrCreateHistogram is the get-or-create variant.
func GetOrCreateHistogram(_ string) Histogram { return Histogram{} }

// GetOrCreateSummary is the get-or-create variant.
func GetOrCreateSummary(_ string) Summary { return Summary{} }
