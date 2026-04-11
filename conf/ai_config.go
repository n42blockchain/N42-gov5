// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Aggregate configuration for the AI infrastructure stack.
// Defines AICfg as a root container plus WalletCfg, CoordCfg,
// GovernanceCfg, TrainingCfg, AttestationCfg and MEVOptimizerCfg
// sub-structs that feed agent wallet, discovery, data governance,
// ZK training verification, inference attestation and AI-driven
// MEV block building. All subsystems default to disabled.

package conf

// AICfg configures all AI infrastructure subsystems.
type AICfg struct {
	// Agent wallet: session keys, spending policies, gas sponsorship
	Wallet WalletCfg `json:"wallet" yaml:"wallet"`
	// Agent coordination: discovery, negotiation, reputation
	Coord CoordCfg `json:"coord" yaml:"coord"`
	// Data governance: ethics committee voting on training datasets
	Governance GovernanceCfg `json:"governance" yaml:"governance"`
	// ZK training verification: model provenance proofs
	Training TrainingCfg `json:"training" yaml:"training"`
	// ZK inference attestation: signed results, chain-of-custody
	Attestation AttestationCfg `json:"attestation" yaml:"attestation"`
	// MEV AI optimizer: transaction ordering, fairness guard
	MEVOptimizer MEVOptimizerCfg `json:"mev_optimizer" yaml:"mev_optimizer"`
}

// WalletCfg configures AI agent wallet management.
type WalletCfg struct {
	Enabled           bool   `json:"enabled" yaml:"enabled"`
	MaxSessionKeys    int    `json:"max_session_keys" yaml:"max_session_keys"`
	DefaultSpendLimit string `json:"default_spend_limit" yaml:"default_spend_limit"`
	PaymasterEnabled  bool   `json:"paymaster_enabled" yaml:"paymaster_enabled"`
}

// CoordCfg configures AI agent coordination and discovery.
type CoordCfg struct {
	Enabled              bool   `json:"enabled" yaml:"enabled"`
	MinAgentStake        string `json:"min_agent_stake" yaml:"min_agent_stake"`
	NegotiationTimeoutSec int   `json:"negotiation_timeout_sec" yaml:"negotiation_timeout_sec"`
	MaxAgentsPerNode     int    `json:"max_agents_per_node" yaml:"max_agents_per_node"`
}

// GovernanceCfg configures AI training data governance.
type GovernanceCfg struct {
	Enabled            bool    `json:"enabled" yaml:"enabled"`
	MaxDatasets        int     `json:"max_datasets" yaml:"max_datasets"`
	CommitteeQuorum    int     `json:"committee_quorum" yaml:"committee_quorum"`
	CommitteeThreshold float64 `json:"committee_threshold" yaml:"committee_threshold"`
}

// TrainingCfg configures ZK training verification.
type TrainingCfg struct {
	Enabled       bool `json:"enabled" yaml:"enabled"`
	MaxProofs     int  `json:"max_proofs" yaml:"max_proofs"`
}

// AttestationCfg configures ZK inference attestation.
type AttestationCfg struct {
	Enabled       bool `json:"enabled" yaml:"enabled"`
	MaxItems      int  `json:"max_items" yaml:"max_items"`
	TTLSec        int  `json:"ttl_sec" yaml:"ttl_sec"`
}

// MEVOptimizerCfg configures AI-enhanced block building.
type MEVOptimizerCfg struct {
	Enabled           bool `json:"enabled" yaml:"enabled"`
	FairnessMode      bool `json:"fairness_mode" yaml:"fairness_mode"`
	OptimizationLevel int  `json:"optimization_level" yaml:"optimization_level"`
	FallbackOnError   bool `json:"fallback_on_error" yaml:"fallback_on_error"`
	WindowSize        int  `json:"window_size" yaml:"window_size"`
}

// DefaultAICfg returns the default AI infrastructure configuration (all disabled).
func DefaultAICfg() AICfg {
	return AICfg{
		Wallet: WalletCfg{
			MaxSessionKeys:    16,
			DefaultSpendLimit: "1000000000000000000",
		},
		Coord: CoordCfg{
			MinAgentStake:         "100000000000000000",
			NegotiationTimeoutSec: 300,
			MaxAgentsPerNode:      100,
		},
		Governance: GovernanceCfg{
			MaxDatasets:        10000,
			CommitteeQuorum:    3,
			CommitteeThreshold: 0.67,
		},
		Training: TrainingCfg{
			MaxProofs: 10000,
		},
		Attestation: AttestationCfg{
			MaxItems: 100000,
			TTLSec:   86400,
		},
		MEVOptimizer: MEVOptimizerCfg{
			FairnessMode:      true,
			OptimizationLevel: 1,
			FallbackOnError:   true,
			WindowSize:        32,
		},
	}
}
