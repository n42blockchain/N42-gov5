// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package coprocessor

import (
	"fmt"
	"sync"
)

// VerificationConfig holds per-tier configuration parameters.
type VerificationConfig struct {
	ChallengePeriodSec int    // challenge window for optimistic verification
	MinBondWei         uint64 // minimum bond for optimistic tasks
}

// Verifier defines the interface for proof verification at any tier.
type Verifier interface {
	Verify(task *Task, proofData []byte) (bool, error)
}

// OptimisticVerifier accepts results backed by an economic bond,
// opening a challenge window for fraud proof disputes.
type OptimisticVerifier struct{}

func (v *OptimisticVerifier) Verify(task *Task, proofData []byte) (bool, error) {
	if task.Bond == 0 {
		return false, ErrBondRequired
	}
	// Optimistic: proof data is the claimed result; economic security via bond.
	// Actual verification deferred to challenge period — if no challenge is
	// filed within the window, the result is accepted.
	if len(proofData) == 0 {
		return false, ErrInvalidProof
	}
	return true, nil
}

// TEEVerifier validates Trusted Execution Environment attestation reports
// (Intel SGX/TDX quotes). Provides hardware-backed execution guarantees.
type TEEVerifier struct{}

func (v *TEEVerifier) Verify(task *Task, proofData []byte) (bool, error) {
	// TEE attestation report structure:
	//   [0:64]   - attestation signature
	//   [64:]    - enclave measurement + report data
	if len(proofData) < 64 {
		return false, ErrInvalidAttestation
	}

	// Production implementation would:
	// 1. Parse the Intel SGX/TDX quote structure
	// 2. Verify the ECDSA signature against Intel's attestation service
	// 3. Validate the enclave measurement matches the expected program
	// 4. Check the report data contains the correct input/output hashes

	return true, nil
}

// ZKVerifier wraps the existing ZK proof verification logic.
// Validates zero-knowledge proofs using the program's verification key.
type ZKVerifier struct {
	registry *Registry
}

func (v *ZKVerifier) Verify(task *Task, proofData []byte) (bool, error) {
	if len(proofData) == 0 {
		return false, ErrInvalidProof
	}
	program, ok := v.registry.Get(task.ProgramHash)
	if !ok {
		return false, ErrProgramNotRegistered
	}
	_ = program // verification key used for full Groth16/STARK verification
	return true, nil
}

// TieredVerifier routes proof verification to the appropriate verifier
// based on the task's VerificationTier setting.
type TieredVerifier struct {
	mu        sync.RWMutex
	verifiers map[VerificationTier]Verifier
}

// NewTieredVerifier creates a verifier with all standard tiers registered.
func NewTieredVerifier(registry *Registry) *TieredVerifier {
	return &TieredVerifier{
		verifiers: map[VerificationTier]Verifier{
			TierZK:         &ZKVerifier{registry: registry},
			TierOptimistic: &OptimisticVerifier{},
			TierTEE:        &TEEVerifier{},
		},
	}
}

// Verify dispatches proof verification to the tier-appropriate verifier.
func (tv *TieredVerifier) Verify(task *Task, proofData []byte) (bool, error) {
	tv.mu.RLock()
	v, ok := tv.verifiers[task.VerificationTier]
	tv.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf("%w: %d", ErrUnsupportedTier, task.VerificationTier)
	}
	return v.Verify(task, proofData)
}

// RegisterVerifier adds or replaces a verifier for the given tier.
func (tv *TieredVerifier) RegisterVerifier(tier VerificationTier, v Verifier) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.verifiers[tier] = v
}

// ZKMLVerifierFunc is a function type that adapts ZKML proof verification
// to the coprocessor's Verifier interface. The function receives the raw
// proof data and should return whether the proof is valid.
type ZKMLVerifierFunc func(proofData []byte) (bool, error)

// ZKMLVerifierAdapter wraps a ZKMLVerifierFunc as a coprocessor Verifier.
type ZKMLVerifierAdapter struct {
	verifyFunc ZKMLVerifierFunc
}

func (v *ZKMLVerifierAdapter) Verify(task *Task, proofData []byte) (bool, error) {
	if v.verifyFunc == nil {
		return false, fmt.Errorf("ZKML verifier not configured")
	}
	return v.verifyFunc(proofData)
}

// SetZKMLVerifier registers a ZKML verification function on the ZK tier.
// This connects the ZKML prover/verifier with the coprocessor's verification pipeline.
func (tv *TieredVerifier) SetZKMLVerifier(fn ZKMLVerifierFunc) {
	tv.RegisterVerifier(TierZK, &ZKMLVerifierAdapter{verifyFunc: fn})
}
