// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// DeferredExecConfig controls the deferred (asynchronous) block execution pipeline.
// When enabled, consensus and execution are decoupled: the consensus layer agrees
// on transaction ordering while execution proceeds asynchronously in the background.
type DeferredExecConfig struct {
	// Enabled activates deferred execution mode. Default: false.
	Enabled bool `json:"deferred_exec" yaml:"deferred_exec"`
	// QueueSize is the max number of blocks queued for execution. Default: 64.
	QueueSize int `json:"deferred_queue_size" yaml:"deferred_queue_size"`
	// Workers is the number of concurrent execution workers. Default: 1.
	Workers int `json:"deferred_workers" yaml:"deferred_workers"`
	// DeepPipeline enables the 5-stage pipeline (prefetch, execute, commit, persist).
	DeepPipeline bool `json:"deep_pipeline" yaml:"deep_pipeline"`
	// PipelineDepth is the channel buffer size for the deep pipeline. Default: 4.
	PipelineDepth int `json:"pipeline_depth" yaml:"pipeline_depth"`
}

// DefaultDeferredExecConfig returns defaults with deferred execution disabled.
func DefaultDeferredExecConfig() DeferredExecConfig {
	return DeferredExecConfig{
		Enabled:   false,
		QueueSize: 64,
		Workers:   1,
	}
}
