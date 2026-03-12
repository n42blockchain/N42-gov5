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
// In production, this would call the ZISK STARK verifier library.
// For now, it validates the proof structure and public inputs format.
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

	// TODO(zisk): STARK verification not yet implemented — accepting all proofs
	// with valid public inputs format. In production, this must call the ZISK
	// STARK verifier library to cryptographically verify the proof:
	//   return zisk.VerifySTARK(proof.ProofData, proof.PublicInputs, guestELFHash)
	starkStubOnce.Do(func() {
		log.Warn("STARK proof verification is a stub — proofs accepted without cryptographic check")
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

	// TODO(zisk): SNARK verification not yet implemented — accepting all proofs
	// with valid public inputs format. In production, this must use gnark-crypto:
	//   vk := loadVerifyingKey()
	//   return groth16.Verify(proof.ProofData, vk, proof.PublicInputs)
	snarkStubOnce.Do(func() {
		log.Warn("SNARK proof verification is a stub — proofs accepted without cryptographic check")
	})

	return nil
}
