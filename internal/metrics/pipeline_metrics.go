// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package metrics

import (
	"time"

	prometheus "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/log"
)

// Pipeline timing metrics track build, import, and commit stages of the
// block production pipeline.
var (
	PipelineBuildMs  = prometheus.GetOrCreateHistogram("n42_pipeline_build_ms")
	PipelineImportMs = prometheus.GetOrCreateHistogram("n42_pipeline_import_ms")
	PipelineCommitMs = prometheus.GetOrCreateHistogram("n42_pipeline_commit_ms")
	PipelineTotalMs  = prometheus.GetOrCreateHistogram("n42_pipeline_total_ms")
	PipelineOverlap  = prometheus.GetOrCreateHistogram("n42_pipeline_overlap_ratio")

	PipelineBlockTxCount  = prometheus.GetOrCreateHistogram("n42_pipeline_block_tx_count")
	PipelineBlockGasUsed  = prometheus.GetOrCreateHistogram("n42_pipeline_block_gas_used")
	PipelineBlockBuildTPS = prometheus.GetOrCreateHistogram("n42_pipeline_block_build_tps")
)

// PipelineTiming tracks timestamps for each pipeline stage.
type PipelineTiming struct {
	Created        time.Time
	BuildComplete  time.Time
	ImportComplete time.Time
	Committed      time.Time
	TxCount        int
	GasUsed        uint64
}

// NewPipelineTiming creates a new PipelineTiming with Created set to now.
func NewPipelineTiming() *PipelineTiming {
	return &PipelineTiming{
		Created: time.Now(),
	}
}

// MarkBuildComplete records the build stage completion time.
func (pt *PipelineTiming) MarkBuildComplete() {
	pt.BuildComplete = time.Now()
}

// MarkImportComplete records the import stage completion time.
func (pt *PipelineTiming) MarkImportComplete() {
	pt.ImportComplete = time.Now()
}

// MarkCommitted records the commit stage completion time.
func (pt *PipelineTiming) MarkCommitted() {
	pt.Committed = time.Now()
}

// Record calculates pipeline timing metrics and records them to Prometheus.
func (pt *PipelineTiming) Record() {
	buildMs := float64(pt.BuildComplete.Sub(pt.Created).Milliseconds())
	importMs := float64(pt.ImportComplete.Sub(pt.BuildComplete).Milliseconds())
	commitMs := float64(pt.Committed.Sub(pt.ImportComplete).Milliseconds())
	totalMs := float64(pt.Committed.Sub(pt.Created).Milliseconds())

	PipelineBuildMs.Observe(buildMs)
	PipelineImportMs.Observe(importMs)
	PipelineCommitMs.Observe(commitMs)
	PipelineTotalMs.Observe(totalMs)

	if totalMs > 0 {
		overlap := (buildMs + importMs) / totalMs
		PipelineOverlap.Observe(overlap)
	}

	PipelineBlockTxCount.Observe(float64(pt.TxCount))
	PipelineBlockGasUsed.Observe(float64(pt.GasUsed))

	if totalMs > 0 {
		tps := float64(pt.TxCount) / (totalMs / 1000.0)
		PipelineBlockBuildTPS.Observe(tps)
	}
}

// LogSummary logs a human-readable summary of the pipeline timing.
func (pt *PipelineTiming) LogSummary() {
	buildMs := pt.BuildComplete.Sub(pt.Created).Milliseconds()
	importMs := pt.ImportComplete.Sub(pt.BuildComplete).Milliseconds()
	commitMs := pt.Committed.Sub(pt.ImportComplete).Milliseconds()
	totalMs := pt.Committed.Sub(pt.Created).Milliseconds()

	log.Info("Pipeline timing",
		"buildMs", buildMs,
		"importMs", importMs,
		"commitMs", commitMs,
		"totalMs", totalMs,
		"txCount", pt.TxCount,
		"gasUsed", pt.GasUsed,
	)
}
