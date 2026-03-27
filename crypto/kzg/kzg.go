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

// Package kzg implements KZG commitment scheme for EIP-4844 blob transactions
// using the go-kzg-4844 library with the Ethereum trusted setup.
//
// Reference: https://eips.ethereum.org/EIPS/eip-4844

package kzg

import (
	"crypto/sha256"
	"errors"
	"runtime"
	"sync"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
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

// Context wraps a go-kzg-4844 context with the Ethereum trusted setup.
type Context struct {
	inner *gokzg4844.Context
}

// InitContext initializes the global KZG context with the Ethereum trusted setup.
func InitContext() error {
	gKZGCtxOnce.Do(func() {
		inner, err := gokzg4844.NewContext4096Secure()
		if err != nil {
			gKZGCtxErr = err
			return
		}
		gKZGCtx = &Context{inner: inner}
	})
	return gKZGCtxErr
}

// GetContext returns the global KZG context.
func GetContext() (*Context, error) {
	if err := InitContext(); err != nil {
		return nil, err
	}
	if gKZGCtx == nil {
		return nil, ErrContextNotInitialized
	}
	return gKZGCtx, nil
}

// =============================================================================
// KZG Operations (package-level convenience functions)
// =============================================================================

// BlobToCommitment computes the KZG commitment for a blob.
func BlobToCommitment(blob *Blob) (Commitment, error) {
	ctx, err := GetContext()
	if err != nil {
		return Commitment{}, err
	}
	return ctx.BlobToCommitment(blob)
}

// ComputeProof computes a KZG proof for a blob at a given evaluation point.
func ComputeProof(blob *Blob, commitment Commitment, point [32]byte) (Proof, [32]byte, error) {
	ctx, err := GetContext()
	if err != nil {
		return Proof{}, [32]byte{}, err
	}
	return ctx.ComputeProof(blob, commitment, point)
}

// VerifyProof verifies a KZG proof at a given point.
func VerifyProof(commitment Commitment, point, claim [32]byte, proof Proof) error {
	ctx, err := GetContext()
	if err != nil {
		return err
	}
	return ctx.VerifyProof(commitment, point, claim, proof)
}

// VerifyBlobProof verifies a blob proof against a commitment.
func VerifyBlobProof(blob *Blob, commitment Commitment, proof Proof) error {
	ctx, err := GetContext()
	if err != nil {
		return err
	}
	return ctx.VerifyBlobProof(blob, commitment, proof)
}

// VerifyBlobProofBatch verifies multiple blob proofs in batch.
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

// BlobToCommitment computes the KZG commitment for a blob using BLS12-381.
func (c *Context) BlobToCommitment(blob *Blob) (Commitment, error) {
	gBlob := (*gokzg4844.Blob)(blob)
	kzgCommitment, err := c.inner.BlobToKZGCommitment(gBlob, runtime.NumCPU())
	if err != nil {
		return Commitment{}, err
	}
	var commitment Commitment
	copy(commitment[:], kzgCommitment[:])
	return commitment, nil
}

// ComputeProof computes a KZG proof for a blob at a given evaluation point.
func (c *Context) ComputeProof(blob *Blob, commitment Commitment, point [32]byte) (Proof, [32]byte, error) {
	gBlob := (*gokzg4844.Blob)(blob)
	kzgProof, claimedValue, err := c.inner.ComputeKZGProof(gBlob, gokzg4844.Scalar(point), runtime.NumCPU())
	if err != nil {
		return Proof{}, [32]byte{}, err
	}
	var proof Proof
	copy(proof[:], kzgProof[:])
	return proof, [32]byte(claimedValue), nil
}

// ComputeBlobProof computes a KZG blob proof for a given blob and commitment.
func (c *Context) ComputeBlobProof(blob *Blob, commitment Commitment) (Proof, error) {
	gBlob := (*gokzg4844.Blob)(blob)
	var kzgCommitment gokzg4844.KZGCommitment
	copy(kzgCommitment[:], commitment[:])
	kzgProof, err := c.inner.ComputeBlobKZGProof(gBlob, kzgCommitment, runtime.NumCPU())
	if err != nil {
		return Proof{}, err
	}
	var proof Proof
	copy(proof[:], kzgProof[:])
	return proof, nil
}

// VerifyProof verifies a KZG proof at a given point.
func (c *Context) VerifyProof(commitment Commitment, point, claim [32]byte, proof Proof) error {
	var kzgCommitment gokzg4844.KZGCommitment
	copy(kzgCommitment[:], commitment[:])
	var kzgProof gokzg4844.KZGProof
	copy(kzgProof[:], proof[:])
	return c.inner.VerifyKZGProof(kzgCommitment, gokzg4844.Scalar(point), gokzg4844.Scalar(claim), kzgProof)
}

// VerifyBlobProof verifies a blob proof against a commitment.
func (c *Context) VerifyBlobProof(blob *Blob, commitment Commitment, proof Proof) error {
	gBlob := (*gokzg4844.Blob)(blob)
	var kzgCommitment gokzg4844.KZGCommitment
	copy(kzgCommitment[:], commitment[:])
	var kzgProof gokzg4844.KZGProof
	copy(kzgProof[:], proof[:])
	return c.inner.VerifyBlobKZGProof(gBlob, kzgCommitment, kzgProof)
}

// VerifyBlobProofBatch verifies multiple blob proofs in batch.
func (c *Context) VerifyBlobProofBatch(blobs []Blob, commitments []Commitment, proofs []Proof) error {
	if len(blobs) != len(commitments) || len(blobs) != len(proofs) {
		return ErrInputLengthMismatch
	}

	gBlobs := make([]gokzg4844.Blob, len(blobs))
	gCommitments := make([]gokzg4844.KZGCommitment, len(commitments))
	gProofs := make([]gokzg4844.KZGProof, len(proofs))

	for i := range blobs {
		gBlobs[i] = gokzg4844.Blob(blobs[i])
		copy(gCommitments[i][:], commitments[i][:])
		copy(gProofs[i][:], proofs[i][:])
	}

	return c.inner.VerifyBlobKZGProofBatch(gBlobs, gCommitments, gProofs)
}

// =============================================================================
// Versioned Hash
// =============================================================================

// CommitmentToVersionedHash converts a commitment to a versioned hash.
func CommitmentToVersionedHash(commitment Commitment) types.Hash {
	h := sha256.Sum256(commitment[:])
	var versionedHash types.Hash
	versionedHash[0] = BlobCommitmentVersionKZG
	copy(versionedHash[1:], h[1:])
	return versionedHash
}

// IsValidVersionedHash checks if a hash has a valid KZG version.
func IsValidVersionedHash(h types.Hash) bool {
	return h[0] == BlobCommitmentVersionKZG
}

// =============================================================================
// Blob Validation
// =============================================================================

// ValidateBlob validates a blob by attempting to deserialize its field elements.
// Each 32-byte field element must be a canonical scalar (< BLS modulus).
func ValidateBlob(blob *Blob) error {
	gBlob := (*gokzg4844.Blob)(blob)
	_, err := gokzg4844.DeserializeBlob(gBlob)
	if err != nil {
		return ErrFieldElementOverflow
	}
	return nil
}

// ValidateBlobSidecar validates a complete blob sidecar.
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
	ErrContextNotInitialized   = errors.New("kzg: context not initialized")
	ErrInvalidCommitment       = errors.New("kzg: invalid commitment")
	ErrCommitmentMismatch      = errors.New("kzg: commitment mismatch")
	ErrInputLengthMismatch     = errors.New("kzg: input length mismatch")
	ErrSidecarMissing          = errors.New("kzg: sidecar missing")
	ErrNoBlobs                 = errors.New("kzg: no blobs in sidecar")
	ErrCommitmentCountMismatch = errors.New("kzg: commitment count mismatch")
	ErrProofCountMismatch      = errors.New("kzg: proof count mismatch")
	ErrHashCountMismatch       = errors.New("kzg: hash count mismatch")
	ErrVersionedHashMismatch   = errors.New("kzg: versioned hash mismatch")
	ErrInvalidProof            = errors.New("kzg: invalid proof")
	ErrFieldElementOverflow    = errors.New("kzg: field element overflow")
)
