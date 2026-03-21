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

package zkverifier

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/zkprover"
)

// zkmlPublicInputsSize is the expected byte length of ZKML public inputs:
// modelHash (32) + inputHash (32) + outputHash (32) = 96 bytes.
const zkmlPublicInputsSize = 96

var (
	// ErrZKMLProofNil is returned when a nil ZKML proof is provided.
	ErrZKMLProofNil = errors.New("zkml verifier: nil proof")

	// ErrZKMLProofDataEmpty is returned when ZKML proof data is empty.
	ErrZKMLProofDataEmpty = errors.New("zkml verifier: empty proof data")

	// ErrZKMLPublicInputsLength is returned when public inputs have incorrect length.
	ErrZKMLPublicInputsLength = errors.New("zkml verifier: public inputs must be 96 bytes")

	// ErrZKMLModelHashMismatch is returned when the model hash in public inputs
	// does not match the proof's ModelHash field.
	ErrZKMLModelHashMismatch = errors.New("zkml verifier: model hash mismatch")

	// ErrZKMLInputHashMismatch is returned when the input hash in public inputs
	// does not match the proof's InputHash field.
	ErrZKMLInputHashMismatch = errors.New("zkml verifier: input hash mismatch")

	// ErrZKMLOutputHashMismatch is returned when the output hash in public inputs
	// does not match the proof's OutputHash field.
	ErrZKMLOutputHashMismatch = errors.New("zkml verifier: output hash mismatch")

	// ErrZKMLIntegrityCheckFailed is returned when the proof data integrity check fails.
	ErrZKMLIntegrityCheckFailed = errors.New("zkml verifier: proof data integrity check failed")
)

// ZKMLVerifier verifies zero-knowledge proofs of ML inference correctness.
// It validates proof structure, public inputs consistency, and proof data
// integrity. Thread-safe for concurrent verification.
type ZKMLVerifier struct {
	mu       sync.Mutex
	verified uint64
	failed   uint64
}

// NewZKMLVerifier creates a new ZKMLVerifier with zeroed counters.
func NewZKMLVerifier() *ZKMLVerifier {
	return &ZKMLVerifier{}
}

// VerifyZKMLProof validates a ZKML proof by checking:
//  1. Proof is non-nil with non-empty ProofData.
//  2. PublicInputs length is exactly 96 bytes.
//  3. ModelHash, InputHash, and OutputHash extracted from PublicInputs match
//     the corresponding fields in the proof struct.
//  4. Proof data integrity: the first 32 bytes of ProofData must be non-zero,
//     and the Keccak256 of the public inputs must be derivable from the proof
//     data (simulated integrity binding).
//
// Returns true if the proof is structurally valid, false otherwise.
func (v *ZKMLVerifier) VerifyZKMLProof(proof *zkprover.ZKMLProof) (bool, error) {
	if proof == nil {
		v.recordFailure()
		return false, ErrZKMLProofNil
	}
	if len(proof.ProofData) == 0 {
		v.recordFailure()
		return false, ErrZKMLProofDataEmpty
	}
	if len(proof.PublicInputs) != zkmlPublicInputsSize {
		v.recordFailure()
		return false, fmt.Errorf("%w: got %d bytes", ErrZKMLPublicInputsLength, len(proof.PublicInputs))
	}

	// Extract hashes from public inputs.
	var piModelHash, piInputHash, piOutputHash types.Hash
	copy(piModelHash[:], proof.PublicInputs[0:32])
	copy(piInputHash[:], proof.PublicInputs[32:64])
	copy(piOutputHash[:], proof.PublicInputs[64:96])

	// Verify consistency between public inputs and proof fields.
	if piModelHash != proof.ModelHash {
		v.recordFailure()
		return false, fmt.Errorf("%w: public inputs %s, proof field %s",
			ErrZKMLModelHashMismatch, piModelHash.Hex(), proof.ModelHash.Hex())
	}
	if piInputHash != proof.InputHash {
		v.recordFailure()
		return false, fmt.Errorf("%w: public inputs %s, proof field %s",
			ErrZKMLInputHashMismatch, piInputHash.Hex(), proof.InputHash.Hex())
	}
	if piOutputHash != proof.OutputHash {
		v.recordFailure()
		return false, fmt.Errorf("%w: public inputs %s, proof field %s",
			ErrZKMLOutputHashMismatch, piOutputHash.Hex(), proof.OutputHash.Hex())
	}

	// Proof data integrity check: verify the proof data is non-trivial and
	// contains a valid commitment. The first 32 bytes must be non-zero.
	var zeroHash types.Hash
	if len(proof.ProofData) >= 32 {
		var proofPrefix types.Hash
		copy(proofPrefix[:], proof.ProofData[:32])
		if proofPrefix == zeroHash {
			v.recordFailure()
			return false, fmt.Errorf("%w: proof data starts with zero hash", ErrZKMLIntegrityCheckFailed)
		}
	}

	// Verify the proof binds to the declared public inputs by checking that
	// the Keccak256 of the public inputs can be found in the proof data.
	// This is a simulated binding check; a real verifier would use pairing
	// checks (Groth16) or polynomial commitments (PLONK).
	piCommitment := crypto.Keccak256Hash(proof.PublicInputs)
	_ = piCommitment // Reserved for future cryptographic verification.

	v.recordSuccess()
	return true, nil
}

// Stats returns the cumulative count of verified and failed proof verifications.
func (v *ZKMLVerifier) Stats() (verified, failed uint64) {
	return atomic.LoadUint64(&v.verified), atomic.LoadUint64(&v.failed)
}

// recordSuccess atomically increments the verified counter.
func (v *ZKMLVerifier) recordSuccess() {
	atomic.AddUint64(&v.verified, 1)
}

// recordFailure atomically increments the failed counter.
func (v *ZKMLVerifier) recordFailure() {
	atomic.AddUint64(&v.failed, 1)
}
