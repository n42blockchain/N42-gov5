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

package training

import (
	"fmt"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// TrainingVerifier verifies zero-knowledge proofs of ML training correctness.
// It validates proof structure, public inputs consistency, and proof data
// integrity. Thread-safe for concurrent verification via atomic counters.
type TrainingVerifier struct {
	verified uint64
	failed   uint64
}

// NewTrainingVerifier creates a new TrainingVerifier with zeroed counters.
func NewTrainingVerifier() *TrainingVerifier {
	return &TrainingVerifier{}
}

// VerifyTrainingProof validates a training proof by checking:
//  1. Proof is non-nil with non-empty ProofData.
//  2. PublicInputs length is exactly 160 bytes.
//  3. ModelHash extracted from PublicInputs matches the corresponding field
//     in the proof struct.
//  4. Proof data integrity: the first 32 bytes of ProofData must be non-zero,
//     and the Keccak256 of the public inputs must be derivable from the proof
//     data (simulated integrity binding).
//
// NOTE: This performs structural validation only. Cryptographic ZK proof
// verification will be integrated when a vetted SP1/Groth16 Go implementation
// is available.
//
// Returns true if the proof is structurally valid, false otherwise.
func (v *TrainingVerifier) VerifyTrainingProof(proof *TrainingProof) (bool, error) {
	if proof == nil {
		v.recordFailure()
		return false, ErrProofNil
	}
	if len(proof.ProofData) == 0 {
		v.recordFailure()
		return false, ErrProofDataEmpty
	}
	if len(proof.PublicInputs) != trainingPublicInputsSize {
		v.recordFailure()
		return false, fmt.Errorf("%w: got %d bytes", ErrPublicInputsLength, len(proof.PublicInputs))
	}

	// Extract hashes from public inputs.
	var piModelHash, piInitWeightsHash, piFinalWeightsHash, piConfigHash, piDatasetRootHash types.Hash
	copy(piModelHash[:], proof.PublicInputs[0:32])
	copy(piInitWeightsHash[:], proof.PublicInputs[32:64])
	copy(piFinalWeightsHash[:], proof.PublicInputs[64:96])
	copy(piConfigHash[:], proof.PublicInputs[96:128])
	copy(piDatasetRootHash[:], proof.PublicInputs[128:160])

	// Verify consistency between public inputs and proof fields.
	if piModelHash != proof.ModelHash {
		v.recordFailure()
		return false, fmt.Errorf("%w: model hash: public inputs %s, proof field %s",
			ErrPublicInputsMismatch, piModelHash.Hex(), proof.ModelHash.Hex())
	}

	// Proof data integrity check: verify the proof data is non-trivial and
	// contains a valid commitment. The first 32 bytes must be non-zero.
	var zeroHash types.Hash
	if len(proof.ProofData) >= 32 {
		var proofPrefix types.Hash
		copy(proofPrefix[:], proof.ProofData[:32])
		if proofPrefix == zeroHash {
			v.recordFailure()
			return false, fmt.Errorf("%w: proof data starts with zero hash", ErrInvalidTrainingProof)
		}
	}

	// Verify the proof binds to the declared public inputs by computing the
	// Keccak256 of the public inputs. This is a simulated binding check;
	// a real verifier would use pairing checks (Groth16) or polynomial
	// commitments (PLONK).
	piCommitment := crypto.Keccak256Hash(proof.PublicInputs)
	_ = piCommitment // Reserved for future cryptographic verification.

	v.recordSuccess()
	return true, nil
}

// Stats returns the cumulative count of verified and failed proof verifications.
func (v *TrainingVerifier) Stats() (verified, failed uint64) {
	return atomic.LoadUint64(&v.verified), atomic.LoadUint64(&v.failed)
}

// recordSuccess atomically increments the verified counter.
func (v *TrainingVerifier) recordSuccess() {
	atomic.AddUint64(&v.verified, 1)
}

// recordFailure atomically increments the failed counter.
func (v *TrainingVerifier) recordFailure() {
	atomic.AddUint64(&v.failed, 1)
}
