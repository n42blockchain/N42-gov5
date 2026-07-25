// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Prometheus metrics for HotStuff-2 consensus health monitoring.
// Counters track metricCurrentView (monotonic per view tick),
// metricBlocksCommitted, metricViewChanges, metricTimeouts,
// metricEquivocations and mxOutputDrops; histograms track the
// per-stage latency breakdown of every committed view (propose,
// proposal delivery, execution wait, Round 1 / Round 2 round-trips
// and the end-to-end view duration). Registered via the
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

	// Set at startup when the recovered consensus state names a committed block
	// that is not on the local canonical chain — the node cannot produce blocks
	// until an operator clears the persisted state (see cmd/hotstuff-reset).
	metricPersistedStateDiverged = prometheus.GetOrCreateCounter("hotstuff_persisted_state_diverged_total", false)

	// Vote counts of the last committed view (gauges).
	metricPrepareVotes = prometheus.GetOrCreateCounter("hotstuff_prepare_votes", true)
	metricCommitVotes  = prometheus.GetOrCreateCounter("hotstuff_commit_votes", true)
)

// Per-view stage latencies of committed views, in seconds (Prometheus base
// unit, so the default buckets .005s–10s cover the whole consensus range).
// See ViewPhases in view_timing.go for what each stage spans; propose is
// leader-only, delivery/exec_wait are follower-only.
var (
	metricViewProposeSeconds  = prometheus.GetOrCreateHistogram("hotstuff_view_propose_seconds")
	metricViewDeliverySeconds = prometheus.GetOrCreateHistogram("hotstuff_view_proposal_delivery_seconds")
	metricViewExecWaitSeconds = prometheus.GetOrCreateHistogram("hotstuff_view_exec_wait_seconds")
	metricViewRound1Seconds   = prometheus.GetOrCreateHistogram("hotstuff_view_round1_seconds")
	metricViewRound2Seconds   = prometheus.GetOrCreateHistogram("hotstuff_view_round2_seconds")
	metricViewTotalSeconds    = prometheus.GetOrCreateHistogram("hotstuff_view_total_seconds")
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

// updateMetricsViewTiming records the per-stage latencies of a committed view.
// Stages that could not be measured on this node (wrong role, missing
// timestamp) are skipped so they do not pollute the distribution with zeros.
func updateMetricsViewTiming(p ViewPhases) {
	observe := func(h interface{ Observe(float64) }, d PhaseDuration) {
		if d.OK {
			h.Observe(d.D.Seconds())
		}
	}
	observe(metricViewProposeSeconds, p.Propose)
	observe(metricViewDeliverySeconds, p.Delivery)
	observe(metricViewExecWaitSeconds, p.ExecWait)
	observe(metricViewRound1Seconds, p.Round1)
	observe(metricViewRound2Seconds, p.Round2)
	observe(metricViewTotalSeconds, p.Total)

	if p.PrepareVoteCount > 0 {
		metricPrepareVotes.Set(uint64(p.PrepareVoteCount))
	}
	if p.CommitVoteCount > 0 {
		metricCommitVotes.Set(uint64(p.CommitVoteCount))
	}
}
