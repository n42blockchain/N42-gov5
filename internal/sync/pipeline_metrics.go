// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Outcomes of one pipeline request (sync_pipeline_requests_total).
const (
	pipeOutcomeOK     = "ok"     // every requested block arrived
	pipeOutcomeShort  = "short"  // a prefix arrived (clean end, cap, cut stream)
	pipeOutcomeEmpty  = "empty"  // no block (refusal after the stream opened, nothing stored)
	pipeOutcomeFailed = "failed" // error before the first block, or a timeout
	pipeOutcomeFault  = "fault"  // a provable fault (malformed, broken link)
	pipeOutcomeDup    = "dup"    // a head-of-line duplicate was issued
)

var (
	syncPipelineMode = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sync_pipeline_mode",
		Help: "Sync mode of the catch-up pipeline: 0 Live, 1 Range (N42_SYNC_PIPELINE).",
	})
	syncPipelineTransitions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sync_pipeline_mode_transitions_total",
		Help: "Sync mode transitions, by the mode entered.",
	}, []string{"to"})
	syncPipelineBufferBlocks = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sync_pipeline_buffer_blocks",
		Help: "Blocks in the catch-up reorder buffer.",
	})
	syncPipelineBufferBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sync_pipeline_buffer_bytes",
		Help: "Estimated decoded bytes in the catch-up reorder buffer.",
	})
	syncPipelineInflight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sync_pipeline_inflight_requests",
		Help: "Range requests the catch-up pipeline has in flight.",
	})
	syncPipelineRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sync_pipeline_requests_total",
		Help: "Range requests issued by the catch-up pipeline, by outcome.",
	}, []string{"outcome"})
	syncPipelineImported = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sync_pipeline_imported_blocks_total",
		Help: "Blocks executed by the catch-up pipeline importer.",
	})
	syncPipelineFallbacks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sync_pipeline_fallbacks_total",
		Help: "Pipeline runs handed to the serial catch-up, by reason.",
	}, []string{"reason"})
	syncPipelineLiveSkipped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sync_pipeline_live_insert_skipped_total",
		Help: "Live-path inserts left to the sync importer in Range mode, by ingress path.",
	}, []string{"path"})
)
