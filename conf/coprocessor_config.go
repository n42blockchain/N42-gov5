// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Distributed compute coprocessor configuration.
// CoprocessorCfg sets concurrency and timeouts for the task scheduler,
// picks the default verification tier (ZK / Optimistic / TEE) with
// challenge window and bond, and controls the provider marketplace
// stake and slashing percentage. DefaultCoprocessorCfg starts fully
// disabled; Validate enforces positive task limits.

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

	// Tiered verification
	DefaultVerificationTier int    `json:"default_verification_tier" yaml:"default_verification_tier"` // 0=ZK, 1=Optimistic, 2=TEE
	OptimisticChallengeSec  int    `json:"optimistic_challenge_sec" yaml:"optimistic_challenge_sec"`
	OptimisticBondWei       uint64 `json:"optimistic_bond_wei" yaml:"optimistic_bond_wei"`

	// Provider marketplace
	EnableMarketplace bool   `json:"enable_marketplace" yaml:"enable_marketplace"`
	MinProviderStake  uint64 `json:"min_provider_stake" yaml:"min_provider_stake"`
	SlashPercentage   int    `json:"slash_percentage" yaml:"slash_percentage"` // 0-100

	// Resident provider: when enabled, this node registers itself as a
	// compute provider and serves claimable WASM tasks with the built-in
	// wazero engine (bytecode comes from the program registry).
	ProviderEnabled    bool   `json:"provider_enabled" yaml:"provider_enabled"`
	ProviderAddress    string `json:"provider_address" yaml:"provider_address"` // hex address, provider identity for stake/rewards
	ProviderStakeWei   uint64 `json:"provider_stake_wei" yaml:"provider_stake_wei"`
	ProviderPollSec    int    `json:"provider_poll_sec" yaml:"provider_poll_sec"`
	ProviderGasLimit   uint64 `json:"provider_gas_limit" yaml:"provider_gas_limit"`
	ProviderEntryPoint string `json:"provider_entry_point" yaml:"provider_entry_point"` // WASM export invoked per task
}

func DefaultCoprocessorCfg() CoprocessorCfg {
	return CoprocessorCfg{
		Enabled:                false,
		MaxConcurrentTasks:     16,
		TaskTimeoutSec:         300,
		MaxPendingTasks:        256,
		PruneIntervalSec:       60,
		DefaultVerificationTier: 0, // TierZK
		OptimisticChallengeSec: 3600,
		OptimisticBondWei:      1_000_000_000_000_000_000, // 1 ETH
		EnableMarketplace:      false,
		MinProviderStake:       10_000_000_000_000_000_000, // 10 ETH
		SlashPercentage:        10,
		ProviderEnabled:        false,
		ProviderStakeWei:       10_000_000_000_000_000_000, // matches MinProviderStake
		ProviderPollSec:        5,
		ProviderGasLimit:       10_000_000,
		ProviderEntryPoint:     "run",
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
	if c.ProviderEnabled {
		if c.ProviderAddress == "" {
			return fmt.Errorf("coprocessor: provider_address required when provider_enabled")
		}
		if c.ProviderStakeWei == 0 {
			return fmt.Errorf("coprocessor: provider_stake_wei must be > 0 when provider_enabled")
		}
		if c.ProviderEntryPoint == "" {
			return fmt.Errorf("coprocessor: provider_entry_point required when provider_enabled")
		}
	}
	return nil
}
