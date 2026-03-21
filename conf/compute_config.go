// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

import "fmt"

// ComputeCfg controls the distributed compute engine including WASM execution,
// batch processing, and AI inference.
type ComputeCfg struct {
	// WASM execution engine
	WASMEnabled        bool    `json:"wasm_enabled" yaml:"wasm_enabled"`
	WASMMaxMemoryMB    int     `json:"wasm_max_memory_mb" yaml:"wasm_max_memory_mb"`
	WASMMaxExecTimeSec int     `json:"wasm_max_exec_time_sec" yaml:"wasm_max_exec_time_sec"`
	WASMGasMultiplier  float64 `json:"wasm_gas_multiplier" yaml:"wasm_gas_multiplier"`

	// Batch compute
	BatchEnabled     bool `json:"batch_enabled" yaml:"batch_enabled"`
	MaxJobTasks      int  `json:"max_job_tasks" yaml:"max_job_tasks"`
	MaxParallelTasks int  `json:"max_parallel_tasks" yaml:"max_parallel_tasks"`

	// AI inference (opML)
	InferenceEnabled bool `json:"inference_enabled" yaml:"inference_enabled"`
	MaxModelSizeMB   int  `json:"max_model_size_mb" yaml:"max_model_size_mb"`
}

// DefaultComputeCfg returns the default compute configuration.
func DefaultComputeCfg() ComputeCfg {
	return ComputeCfg{
		WASMEnabled:        false,
		WASMMaxMemoryMB:    256,
		WASMMaxExecTimeSec: 30,
		WASMGasMultiplier:  1.0,
		BatchEnabled:       false,
		MaxJobTasks:        1024,
		MaxParallelTasks:   16,
		InferenceEnabled:   false,
		MaxModelSizeMB:     512,
	}
}

// Validate checks the compute configuration for errors.
func (c *ComputeCfg) Validate() error {
	if c.WASMEnabled {
		if c.WASMMaxMemoryMB <= 0 {
			return fmt.Errorf("compute: wasm_max_memory_mb must be > 0")
		}
		if c.WASMMaxExecTimeSec <= 0 {
			return fmt.Errorf("compute: wasm_max_exec_time_sec must be > 0")
		}
		if c.WASMGasMultiplier <= 0 {
			return fmt.Errorf("compute: wasm_gas_multiplier must be > 0")
		}
	}
	if c.BatchEnabled {
		if c.MaxJobTasks <= 0 {
			return fmt.Errorf("compute: max_job_tasks must be > 0")
		}
		if c.MaxParallelTasks <= 0 {
			return fmt.Errorf("compute: max_parallel_tasks must be > 0")
		}
	}
	return nil
}
