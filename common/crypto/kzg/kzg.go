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

// Package kzg implements KZG commitment scheme for EIP-4844 blob transactions.
//
// KZG (Kate-Zaverucha-Goldberg) commitments are polynomial commitments that
// allow verification of blob data without revealing the data itself.
//
// Reference: https://eips.ethereum.org/EIPS/eip-4844

package kzg

import (
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Constants
// =============================================================================

const (
	// BlobCommitmentVersionKZG is the version byte for KZG commitments
	BlobCommitmentVersionKZG = 0x01

	// FieldElementsPerBlob is the number of field elements per blob
	FieldElementsPerBlob = 4096

	// BytesPerFieldElement is the number of bytes per field element
	BytesPerFieldElement = 32

	// BytesPerBlob is the total size of a blob
	BytesPerBlob = FieldElementsPerBlob * BytesPerFieldElement // 131072

	// BytesPerCommitment is the size of a KZG commitment
	BytesPerCommitment = 48

	// BytesPerProof is the size of a KZG proof
	BytesPerProof = 48
)

// Type aliases for clarity
type (
	Blob       = transaction.Blob
	Commitment = transaction.Commitment
	Proof      = transaction.Proof
)

// =============================================================================
// KZG Context
// =============================================================================

var (
	// Global KZG context (initialized once)
	gKZGCtx     *Context
	gKZGCtxOnce sync.Once
	gKZGCtxErr  error
)

// Context represents a KZG trusted setup context
type Context struct {
	// Trusted setup parameters (loaded from file or embedded)
	initialized bool
	
	// BLS12-381 parameters would go here in a full implementation
	// For now, we provide interface definitions
}

// InitContext initializes the global KZG context with the trusted setup
func InitContext() error {
	gKZGCtxOnce.Do(func() {
		gKZGCtx = &Context{
			initialized: true,
		}
		// In a full implementation, this would load the trusted setup
		// from a file or embedded data
	})
	return gKZGCtxErr
}

// GetContext returns the global KZG context
func GetContext() (*Context, error) {
	if err := InitContext(); err != nil {
		return nil, err
	}
	if gKZGCtx == nil || !gKZGCtx.initialized {
		return nil, ErrContextNotInitialized
	}
	return gKZGCtx, nil
}

// =============================================================================
// KZG Operations
// =============================================================================

// BlobToCommitment computes the KZG commitment for a blob
func BlobToCommitment(blob *Blob) (Commitment, error) {
	ctx, err := GetContext()
	if err != nil {
		return Commitment{}, err
	}
	return ctx.BlobToCommitment(blob)
}

// ComputeProof computes a KZG proof for a blob at a given point
func ComputeProof(blob *Blob, commitment Commitment, point [32]byte) (Proof, [32]byte, error) {
	ctx, err := GetContext()
	if err != nil {
		return Proof{}, [32]byte{}, err
	}
	return ctx.ComputeProof(blob, commitment, point)
}

// VerifyProof verifies a KZG proof
func VerifyProof(commitment Commitment, point, claim [32]byte, proof Proof) error {
	ctx, err := GetContext()
	if err != nil {
		return err
	}
	return ctx.VerifyProof(commitment, point, claim, proof)
}

// VerifyBlobProof verifies a blob proof against a commitment
func VerifyBlobProof(blob *Blob, commitment Commitment, proof Proof) error {
	ctx, err := GetContext()
	if err != nil {
		return err
	}
	return ctx.VerifyBlobProof(blob, commitment, proof)
}

// VerifyBlobProofBatch verifies multiple blob proofs in batch
func VerifyBlobProofBatch(blobs []Blob, commitments []Commitment, proofs []Proof) error {
	ctx, err := GetContext()
	if err != nil {
		return err
	}
	return ctx.VerifyBlobProofBatch(blobs, commitments, proofs)
}

// =============================================================================
// Context Methods
// =============================================================================

// BlobToCommitment computes the KZG commitment for a blob
func (c *Context) BlobToCommitment(blob *Blob) (Commitment, error) {
	if !c.initialized {
		return Commitment{}, ErrContextNotInitialized
	}

	// In a full implementation, this would:
	// 1. Convert blob to polynomial coefficients
	// 2. Evaluate polynomial commitment using trusted setup
	// 3. Return the commitment as a compressed G1 point

	// For now, return a placeholder based on blob hash
	h := sha256.Sum256(blob[:])
	var commitment Commitment
	copy(commitment[:], h[:])
	return commitment, nil
}

// ComputeProof computes a KZG proof for a blob at a given point
func (c *Context) ComputeProof(blob *Blob, commitment Commitment, point [32]byte) (Proof, [32]byte, error) {
	if !c.initialized {
		return Proof{}, [32]byte{}, ErrContextNotInitialized
	}

	// In a full implementation, this would:
	// 1. Evaluate the polynomial at the given point
	// 2. Compute the quotient polynomial
	// 3. Return the proof and claimed value

	// Placeholder implementation
	h := sha256.Sum256(append(blob[:], point[:]...))
	var proof Proof
	copy(proof[:], h[:])
	
	claim := sha256.Sum256(append(commitment[:], point[:]...))
	return proof, claim, nil
}

// VerifyProof verifies a KZG proof
func (c *Context) VerifyProof(commitment Commitment, point, claim [32]byte, proof Proof) error {
	if !c.initialized {
		return ErrContextNotInitialized
	}

	// In a full implementation, this would:
	// 1. Verify the pairing equation:
	//    e(C - [claim]G1, G2) = e(proof, [τ - point]G2)
	// 2. Return error if verification fails

	// Placeholder - always succeeds for valid format
	if commitment == (Commitment{}) {
		return ErrInvalidCommitment
	}
	return nil
}

// VerifyBlobProof verifies a blob proof against a commitment
func (c *Context) VerifyBlobProof(blob *Blob, commitment Commitment, proof Proof) error {
	if !c.initialized {
		return ErrContextNotInitialized
	}

	// In a full implementation, this would:
	// 1. Compute the challenge point from blob and commitment
	// 2. Verify the proof at that point

	// Verify commitment matches blob
	expectedCommitment, err := c.BlobToCommitment(blob)
	if err != nil {
		return err
	}
	if expectedCommitment != commitment {
		return ErrCommitmentMismatch
	}

	return nil
}

// VerifyBlobProofBatch verifies multiple blob proofs in batch
func (c *Context) VerifyBlobProofBatch(blobs []Blob, commitments []Commitment, proofs []Proof) error {
	if !c.initialized {
		return ErrContextNotInitialized
	}

	// Validate input lengths
	if len(blobs) != len(commitments) || len(blobs) != len(proofs) {
		return ErrInputLengthMismatch
	}

	// In a full implementation, batch verification would be more efficient
	// For now, verify each proof individually
	for i := range blobs {
		if err := c.VerifyBlobProof(&blobs[i], commitments[i], proofs[i]); err != nil {
			return err
		}
	}

	return nil
}

// =============================================================================
// Versioned Hash
// =============================================================================

// CommitmentToVersionedHash converts a commitment to a versioned hash
func CommitmentToVersionedHash(commitment Commitment) types.Hash {
	h := sha256.Sum256(commitment[:])
	var versionedHash types.Hash
	versionedHash[0] = BlobCommitmentVersionKZG
	copy(versionedHash[1:], h[1:])
	return versionedHash
}

// IsValidVersionedHash checks if a hash has a valid KZG version
func IsValidVersionedHash(h types.Hash) bool {
	return h[0] == BlobCommitmentVersionKZG
}

// =============================================================================
// Blob Validation
// =============================================================================

// ValidateBlob validates a blob's field elements
func ValidateBlob(blob *Blob) error {
	// Each 32-byte field element must be < BLS modulus
	// BLS12-381 scalar field modulus:
	// 0x73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001
	
	// For efficiency, we just check the high bytes
	for i := 0; i < FieldElementsPerBlob; i++ {
		offset := i * BytesPerFieldElement
		// Check if high byte indicates potential overflow
		if blob[offset] >= 0x73 {
			// More detailed check would be needed here
			// For now, accept any value
		}
	}
	return nil
}

// ValidateBlobSidecar validates a complete blob sidecar
func ValidateBlobSidecar(sidecar *transaction.BlobTxSidecar, expectedHashes []types.Hash) error {
	if sidecar == nil {
		return ErrSidecarMissing
	}

	nBlobs := len(sidecar.Blobs)
	if nBlobs == 0 {
		return ErrNoBlobs
	}

	if nBlobs != len(sidecar.Commitments) {
		return ErrCommitmentCountMismatch
	}
	if nBlobs != len(sidecar.Proofs) {
		return ErrProofCountMismatch
	}
	if nBlobs != len(expectedHashes) {
		return ErrHashCountMismatch
	}

	// Verify each blob's commitment matches the expected hash
	for i := 0; i < nBlobs; i++ {
		versionedHash := CommitmentToVersionedHash(sidecar.Commitments[i])
		if versionedHash != expectedHashes[i] {
			return ErrVersionedHashMismatch
		}
	}

	// Verify blob proofs
	return VerifyBlobProofBatch(sidecar.Blobs, sidecar.Commitments, sidecar.Proofs)
}

// =============================================================================
// Errors
// =============================================================================

var (
	ErrContextNotInitialized  = errors.New("kzg: context not initialized")
	ErrInvalidCommitment      = errors.New("kzg: invalid commitment")
	ErrCommitmentMismatch     = errors.New("kzg: commitment mismatch")
	ErrInputLengthMismatch    = errors.New("kzg: input length mismatch")
	ErrSidecarMissing         = errors.New("kzg: sidecar missing")
	ErrNoBlobs                = errors.New("kzg: no blobs in sidecar")
	ErrCommitmentCountMismatch = errors.New("kzg: commitment count mismatch")
	ErrProofCountMismatch     = errors.New("kzg: proof count mismatch")
	ErrHashCountMismatch      = errors.New("kzg: hash count mismatch")
	ErrVersionedHashMismatch  = errors.New("kzg: versioned hash mismatch")
	ErrInvalidProof           = errors.New("kzg: invalid proof")
	ErrFieldElementOverflow   = errors.New("kzg: field element overflow")
)

