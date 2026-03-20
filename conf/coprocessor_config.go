// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

import "fmt"

// CoprocessorCfg controls the ZK coprocessor service that allows smart
// contracts to offload heavy computation off-chain and verify results on-chain.
type CoprocessorCfg struct {
	Enabled            bool   `json:"enabled" yaml:"enabled"`
	MaxConcurrentTasks int    `json:"max_concurrent_tasks" yaml:"max_concurrent_tasks"`
	TaskTimeoutSec     int    `json:"task_timeout" yaml:"task_timeout"`
	MaxPendingTasks    int    `json:"max_pending_tasks" yaml:"max_pending_tasks"`
	ProverEndpoint     string `json:"prover_endpoint" yaml:"prover_endpoint"`
	PruneIntervalSec   int    `json:"prune_interval" yaml:"prune_interval"`
}

func DefaultCoprocessorCfg() CoprocessorCfg {
	return CoprocessorCfg{
		Enabled:            false,
		MaxConcurrentTasks: 16,
		TaskTimeoutSec:     300,
		MaxPendingTasks:    256,
		PruneIntervalSec:   60,
	}
}

func (c *CoprocessorCfg) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.MaxConcurrentTasks <= 0 {
		return fmt.Errorf("coprocessor: max_concurrent_tasks must be > 0")
	}
	if c.TaskTimeoutSec <= 0 {
		return fmt.Errorf("coprocessor: task_timeout must be > 0")
	}
	return nil
}
