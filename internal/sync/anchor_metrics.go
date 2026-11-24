// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Refusal reasons of the S1 anchor gate (sync_anchor_would_refuse_total).
// In observe mode they count what enforce would refuse; in enforce mode they
// count what was refused.
const (
	anchorReasonUnanchored    = "unanchored"     // a block above the highest anchor
	anchorReasonBrokenLink    = "broken_link"    // non-contiguous number or parent hash in a range
	anchorReasonBadQC         = "bad_qc"         // a QC failing verification against its epoch's set
	anchorReasonUnknownEpoch  = "unknown_epoch"  // no validator set for the QC's epoch
	anchorReasonPrepareDomain = "prepare_domain" // a QC valid only in the prepare/vote domain
	anchorReasonBadExtra      = "bad_extra"      // an N42H header whose Extra does not decode
)

var (
	syncAnchorWouldRefuse = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sync_anchor_would_refuse_total",
			Help: "Catch-up blocks or header QCs the S1 anchor gate refuses (enforce) or would refuse (observe), by reason.",
		}, []string{"reason"},
	)
	syncAnchorOK = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "sync_anchor_ok_total",
			Help: "Catch-up blocks hash-linked to a verified commit-domain QC.",
		},
	)
	syncAnchorQCVerifications = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "sync_anchor_qc_verifications_total",
			Help: "Header QCs the S1 anchor gate verified with BLS (cache misses).",
		},
	)
	syncAnchorTailEvicted = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "sync_anchor_tail_evicted_total",
			Help: "Unanchored tail blocks dropped by the 64-block / 512 MiB bound (enforce).",
		},
	)
	syncAnchorTailBlocks = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "sync_anchor_tail_blocks",
			Help: "Unanchored catch-up blocks held unexecuted (enforce).",
		},
	)
)

func countAnchorRefusal(reason string, n int) {
	if n > 0 {
		syncAnchorWouldRefuse.WithLabelValues(reason).Add(float64(n))
	}
}
