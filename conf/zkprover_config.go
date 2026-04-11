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
//
// ZK prover subsystem configuration.
// ZKProverCfg wires the external ZISK prover cluster: ProverAddr
// gRPC endpoint, ProverTimeout, ProofType ("stark" / "snark"),
// MaxConcurrent proving jobs and the GuestBinary path for the
// RISC-V64 zkVM guest used by cmd/zkguest to trace execution.

package conf

import "fmt"

// ZKProverCfg holds configuration for the ZK proof generation and verification subsystem.
type ZKProverCfg struct {
	// Enabled turns on ZK proof generation for locally produced blocks.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// ProverAddr is the gRPC address of the external ZISK prover cluster.
	ProverAddr string `json:"prover_addr" yaml:"prover_addr"`

	// ProverTimeout is the maximum time in seconds to wait for a proof (default: 600).
	ProverTimeout int `json:"prover_timeout" yaml:"prover_timeout"`

	// ProofType specifies the proof system: "stark" or "snark".
	ProofType string `json:"proof_type" yaml:"proof_type"`

	// MaxConcurrent limits the number of concurrent proof generation jobs.
	MaxConcurrent int `json:"max_concurrent" yaml:"max_concurrent"`

	// GuestBinary is the path to the RISC-V64 ELF binary for the zkVM guest program.
	GuestBinary string `json:"guest_binary" yaml:"guest_binary"`

	// VerifyOnly enables proof verification without generation.
	// Nodes in this mode verify proofs on received blocks but do not generate them.
	VerifyOnly bool `json:"verify_only" yaml:"verify_only"`

	// RequireProof when true, rejects blocks without valid ZK proofs.
	// When false (default), blocks without proofs are processed normally via EVM execution.
	RequireProof bool `json:"require_proof" yaml:"require_proof"`

	// SP1 prover network configuration.
	// SP1CLIPath is the path to the sp1 CLI binary (default: "sp1" in PATH).
	SP1CLIPath string `json:"sp1_cli_path" yaml:"sp1_cli_path"`
	// SP1APIEndpoint is the Succinct Prover Network REST API endpoint.
	SP1APIEndpoint string `json:"sp1_api_endpoint" yaml:"sp1_api_endpoint"`
	// SP1APIKey is the API key for Succinct Network authentication.
	SP1APIKey string `json:"sp1_api_key" yaml:"sp1_api_key"`
}

// Validate checks that the ZKProverCfg fields are consistent.
func (c *ZKProverCfg) Validate() error {
	if !c.Enabled && !c.VerifyOnly {
		return nil // disabled, nothing to validate
	}

	if c.ProofType != "" && c.ProofType != "stark" && c.ProofType != "snark" {
		return fmt.Errorf("zkprover: invalid proof_type %q (must be \"stark\" or \"snark\")", c.ProofType)
	}

	if c.Enabled && !c.VerifyOnly {
		if c.ProverAddr == "" {
			return fmt.Errorf("zkprover: prover_addr is required when enabled")
		}
		if c.MaxConcurrent < 0 {
			return fmt.Errorf("zkprover: max_concurrent must be >= 0 (0 = unlimited)")
		}
		if c.ProverTimeout <= 0 {
			return fmt.Errorf("zkprover: prover_timeout must be > 0")
		}
	}

	return nil
}

// DefaultZKProverCfg returns sensible defaults for ZK prover configuration.
func DefaultZKProverCfg() ZKProverCfg {
	return ZKProverCfg{
		Enabled:       false,
		ProverAddr:    "localhost:50051",
		ProverTimeout: 600,
		ProofType:     "stark",
		MaxConcurrent: 2,
		GuestBinary:   "build/bin/zkguest",
		VerifyOnly:    false,
		RequireProof:  false,
		SP1CLIPath:    "sp1",
	}
}
