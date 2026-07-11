// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Prometheus counters for HotStuff-2 consensus health monitoring.
// Tracks metricCurrentView (monotonic per view tick),
// metricBlocksCommitted, metricViewChanges, metricTimeouts,
// metricEquivocations and mxOutputDrops. Registered via the
// common/metrics wrapper so they appear on the standard /metrics
// endpoint alongside other subsystem counters.

package hotstuff

import (
	prometheus "github.com/n42blockchain/N42/common/metrics"
)

// HotStuff consensus Prometheus metrics.
var (
	metricCurrentView         = prometheus.GetOrCreateCounter("hotstuff_current_view", true)
	metricBlocksCommitted     = prometheus.GetOrCreateCounter("hotstuff_blocks_committed", false)
	metricViewChanges         = prometheus.GetOrCreateCounter("hotstuff_view_changes", false)
	metricTimeouts            = prometheus.GetOrCreateCounter("hotstuff_timeouts", false)
	metricEquivocations       = prometheus.GetOrCreateCounter("hotstuff_equivocations", false)
	metricCommittedUnexecuted = prometheus.GetOrCreateCounter("hotstuff_committed_unexecuted_total", false)
	mxOutputDrops             = prometheus.GetOrCreateCounter("hotstuff_output_drops", false)
)

func updateMetricsBlockCommitted(view ViewNumber) {
	metricCurrentView.Set(view)
	metricBlocksCommitted.Inc()
}

func updateMetricsViewChanged(view ViewNumber) {
	metricCurrentView.Set(view)
	metricViewChanges.Inc()
}

func updateMetricsTimeout() {
	metricTimeouts.Inc()
}

func updateMetricsEquivocation() {
	metricEquivocations.Inc()
}
