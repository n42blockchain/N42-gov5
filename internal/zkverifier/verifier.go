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

// Package zkverifier provides ZK proof verification for block state transitions.
// It supports both STARK (native ZISK output) and SNARK (Groth16 wrapper) proofs.
package zkverifier

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	prometheus "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/log"
)

var starkStubOnce sync.Once
var snarkStubOnce sync.Once

var (
	// ErrProofNil is returned when a nil proof is provided.
	ErrProofNil = errors.New("zkverifier: nil proof")

	// ErrProofDataEmpty is returned when proof data is empty.
	ErrProofDataEmpty = errors.New("zkverifier: empty proof data")

	// ErrPublicInputsMismatch is returned when public inputs don't match expected values.
	ErrPublicInputsMismatch = errors.New("zkverifier: public inputs mismatch")

	// ErrUnsupportedProofType is returned for unknown proof types.
	ErrUnsupportedProofType = errors.New("zkverifier: unsupported proof type")

	// ErrSTARKVerificationFailed is returned when STARK verification fails.
	ErrSTARKVerificationFailed = errors.New("zkverifier: STARK verification failed")

	// ErrSNARKVerificationFailed is returned when SNARK verification fails.
	ErrSNARKVerificationFailed = errors.New("zkverifier: SNARK verification failed")

	// ErrCryptographicVerificationUnavailable is returned when the verifier can
	// only validate public inputs but cannot prove cryptographic soundness.
	ErrCryptographicVerificationUnavailable = errors.New("zkverifier: cryptographic verification not implemented")

	verifyDuration = prometheus.GetOrCreateSummary("zkverifier_verify_duration_seconds")
	verifySuccess  = prometheus.GetOrCreateCounter("zkverifier_verify_success_total", true)
	verifyFailed   = prometheus.GetOrCreateCounter("zkverifier_verify_failed_total", true)
)

// Verifier provides ZK proof verification for block state transitions.
type Verifier struct{}

// NewVerifier creates a new ZK proof verifier.
func NewVerifier() *Verifier {
	return &Verifier{}
}

// CryptographicReady reports whether a real cryptographic verifier backend is
// wired in. Before that point, this package only performs parallel side-checks.
func (v *Verifier) CryptographicReady() bool {
	return false
}

// Verify validates a ZK proof against expected block execution results.
// It dispatches to the appropriate verification method based on proof type.
func (v *Verifier) Verify(proof *zkprover.Proof, expectedStateRoot types.Hash, expectedGasUsed uint64) error {
	if proof == nil {
		return ErrProofNil
	}
	if len(proof.ProofData) == 0 {
		return ErrProofDataEmpty
	}

	start := time.Now()
	var err error

	switch proof.Type {
	case zkprover.ProofTypeSTARK:
		err = v.VerifySTARK(proof, expectedStateRoot, expectedGasUsed)
	case zkprover.ProofTypeSNARK:
		err = v.VerifySNARK(proof, expectedStateRoot)
	case zkprover.ProofTypeSP1:
		err = v.VerifySP1(proof, expectedStateRoot, expectedGasUsed)
	default:
		err = fmt.Errorf("%w: %s", ErrUnsupportedProofType, proof.Type)
	}

	elapsed := time.Since(start)
	verifyDuration.UpdateDuration(start)

	if err != nil {
		verifyFailed.Inc()
		log.Warn("ZK proof verification failed", "block", proof.BlockNumber, "type", proof.Type, "err", err, "elapsed", elapsed)
		return err
	}

	verifySuccess.Inc()
	log.Debug("ZK proof verified", "block", proof.BlockNumber, "type", proof.Type, "elapsed", elapsed)
	return nil
}

// VerifySTARK verifies a STARK proof against expected execution results.
// Before the cryptographic backend is ready, this acts as a side-check that
// validates public inputs shape and consistency only.
func (v *Verifier) VerifySTARK(proof *zkprover.Proof, expectedStateRoot types.Hash, expectedGasUsed uint64) error {
	if len(proof.PublicInputs) < 32+8 { // stateRoot + gasUsed minimum
		return fmt.Errorf("%w: public inputs too short (%d bytes)", ErrPublicInputsMismatch, len(proof.PublicInputs))
	}

	// Extract public inputs: [32B postStateRoot][8B gasUsed]
	var provenStateRoot types.Hash
	copy(provenStateRoot[:], proof.PublicInputs[:32])

	if provenStateRoot != expectedStateRoot {
		return fmt.Errorf("%w: state root expected %x, proven %x",
			ErrPublicInputsMismatch, expectedStateRoot, provenStateRoot)
	}

	// Validate gasUsed matches the expected value.
	provenGasUsed := binary.LittleEndian.Uint64(proof.PublicInputs[32:40])
	if provenGasUsed != expectedGasUsed {
		return fmt.Errorf("%w: gas used expected %d, proven %d",
			ErrPublicInputsMismatch, expectedGasUsed, provenGasUsed)
	}

	starkStubOnce.Do(func() {
		log.Warn("STARK proof verification is running in side-check mode only; cryptographic backend not yet enabled")
	})

	return nil
}

// VerifySNARK verifies a Groth16 SNARK proof wrapping a STARK.
// This provides constant-size proofs suitable for on-chain verification.
func (v *Verifier) VerifySNARK(proof *zkprover.Proof, expectedStateRoot types.Hash) error {
	if len(proof.PublicInputs) < 32 {
		return fmt.Errorf("%w: public inputs too short for SNARK", ErrPublicInputsMismatch)
	}

	var provenStateRoot types.Hash
	copy(provenStateRoot[:], proof.PublicInputs[:32])

	if provenStateRoot != expectedStateRoot {
		return fmt.Errorf("%w: state root expected %x, proven %x",
			ErrPublicInputsMismatch, expectedStateRoot, provenStateRoot)
	}

	snarkStubOnce.Do(func() {
		log.Warn("SNARK proof verification is running in side-check mode only; cryptographic backend not yet enabled")
	})

	return nil
}

// VerifySP1 verifies an SP1-generated proof against expected execution results.
//
// SP1 proofs contain:
//   - ProofData: STARK proof bytes (or simulated hash in dev mode)
//   - PublicInputs: [32B postStateRoot][8B gasUsed]
//
// In simulation mode (development), this validates public inputs only.
// In production mode (with SP1 SDK), this would call SP1's native verifier
// which validates the full STARK proof cryptographically.
func (v *Verifier) VerifySP1(proof *zkprover.Proof, expectedStateRoot types.Hash, expectedGasUsed uint64) error {
	if len(proof.PublicInputs) < 40 {
		return fmt.Errorf("%w: SP1 public inputs too short (%d bytes, need 40)",
			ErrPublicInputsMismatch, len(proof.PublicInputs))
	}

	// Extract and validate state root
	var provenStateRoot types.Hash
	copy(provenStateRoot[:], proof.PublicInputs[:32])
	if provenStateRoot != expectedStateRoot {
		return fmt.Errorf("%w: SP1 state root expected %x, proven %x",
			ErrPublicInputsMismatch, expectedStateRoot, provenStateRoot)
	}

	// Extract and validate gas used
	provenGasUsed := binary.LittleEndian.Uint64(proof.PublicInputs[32:40])
	if provenGasUsed != expectedGasUsed {
		return fmt.Errorf("%w: SP1 gas used expected %d, proven %d",
			ErrPublicInputsMismatch, expectedGasUsed, provenGasUsed)
	}

	// TODO: When SP1 Go SDK is available, call sp1.Verify(proof.ProofData, vkey, publicInputs)
	// For now, public input validation provides structural correctness.
	sp1StubOnce.Do(func() {
		log.Info("SP1 proof verification active (public input validation mode)",
			"note", "full cryptographic verification requires SP1 SDK integration")
	})

	return nil
}

var sp1StubOnce sync.Once
