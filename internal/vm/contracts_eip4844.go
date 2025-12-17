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

// EIP-4844: Shard Blob Transactions - Precompiled Contracts
// Reference: https://eips.ethereum.org/EIPS/eip-4844

package vm

import (
	"crypto/sha256"
	"errors"

	"github.com/n42blockchain/N42/common/crypto/kzg"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Point Evaluation Precompile (EIP-4844)
// =============================================================================

// PointEvaluationPrecompileAddress is the address of the point evaluation precompile
var PointEvaluationPrecompileAddress = types.HexToAddress("0x000000000000000000000000000000000000000a")

// Point evaluation input sizes
const (
	// Input format: versioned_hash (32) + z (32) + y (32) + commitment (48) + proof (48) = 192 bytes
	pointEvaluationInputLength = 192
	
	// Output is 64 bytes: FIELD_ELEMENTS_PER_BLOB (32) + BLS_MODULUS (32)
	pointEvaluationOutputLength = 64
)

// BLS12-381 scalar field modulus
// 0x73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001
var blsModulus = [32]byte{
	0x73, 0xed, 0xa7, 0x53, 0x29, 0x9d, 0x7d, 0x48,
	0x33, 0x39, 0xd8, 0x08, 0x09, 0xa1, 0xd8, 0x05,
	0x53, 0xbd, 0xa4, 0x02, 0xff, 0xfe, 0x5b, 0xfe,
	0xff, 0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x01,
}

// pointEvaluationPrecompile implements the KZG point evaluation precompile
// introduced in EIP-4844.
type pointEvaluationPrecompile struct{}

// RequiredGas returns the gas required to execute the precompiled contract
func (c *pointEvaluationPrecompile) RequiredGas(input []byte) uint64 {
	return params.BlobTxPointEvaluationPrecompileGas
}

// Run executes the point evaluation precompile
//
// Input format (192 bytes):
//   - versioned_hash: 32 bytes - The versioned hash of the blob commitment
//   - z: 32 bytes - The evaluation point
//   - y: 32 bytes - The claimed evaluation result
//   - commitment: 48 bytes - The KZG commitment
//   - proof: 48 bytes - The KZG proof
//
// Output format (64 bytes):
//   - FIELD_ELEMENTS_PER_BLOB: 32 bytes (big-endian)
//   - BLS_MODULUS: 32 bytes (big-endian)
func (c *pointEvaluationPrecompile) Run(input []byte) ([]byte, error) {
	if len(input) != pointEvaluationInputLength {
		return nil, errBlobVerifyInputLength
	}

	// Parse input
	var (
		versionedHash types.Hash
		z             [32]byte
		y             [32]byte
		commitment    transaction.Commitment
		proof         transaction.Proof
	)

	copy(versionedHash[:], input[0:32])
	copy(z[:], input[32:64])
	copy(y[:], input[64:96])
	copy(commitment[:], input[96:144])
	copy(proof[:], input[144:192])

	// Verify versioned hash matches commitment
	if err := verifyVersionedHash(versionedHash, commitment); err != nil {
		return nil, err
	}

	// Verify the KZG proof
	if err := kzg.VerifyProof(commitment, z, y, proof); err != nil {
		return nil, errBlobVerifyKZGProof
	}

	// Return success: FIELD_ELEMENTS_PER_BLOB || BLS_MODULUS
	output := make([]byte, pointEvaluationOutputLength)
	
	// FIELD_ELEMENTS_PER_BLOB = 4096 as 32-byte big-endian
	output[31] = byte(kzg.FieldElementsPerBlob & 0xff)
	output[30] = byte((kzg.FieldElementsPerBlob >> 8) & 0xff)
	
	// BLS_MODULUS as 32-byte big-endian
	copy(output[32:64], blsModulus[:])

	return output, nil
}

// verifyVersionedHash verifies that the versioned hash matches the commitment
func verifyVersionedHash(versionedHash types.Hash, commitment transaction.Commitment) error {
	// Check version byte
	if versionedHash[0] != transaction.VersionedHashVersionKZG {
		return errBlobVerifyVersionHash
	}

	// Compute expected versioned hash from commitment
	expected := kzg.CommitmentToVersionedHash(commitment)
	if versionedHash != expected {
		return errBlobVerifyMismatch
	}

	return nil
}

// =============================================================================
// Precompile Errors
// =============================================================================

var (
	errBlobVerifyInputLength = errors.New("invalid input length for point evaluation")
	errBlobVerifyVersionHash = errors.New("invalid versioned hash version")
	errBlobVerifyMismatch    = errors.New("versioned hash mismatch")
	errBlobVerifyKZGProof    = errors.New("kzg proof verification failed")
)

// =============================================================================
// Precompile Registration
// =============================================================================

// init registers the point evaluation precompile for Cancun
func init() {
	// The precompile is registered in the Cancun precompile set
	// See PrecompiledContractsCancun in contracts.go
}

// GetPointEvaluationPrecompile returns the point evaluation precompile instance
func GetPointEvaluationPrecompile() PrecompiledContract {
	return &pointEvaluationPrecompile{}
}

// =============================================================================
// Blob Hash Computation (for BLOBHASH opcode)
// =============================================================================

// ComputeBlobHash computes the versioned hash for a blob
func ComputeBlobHash(blob *transaction.Blob) (types.Hash, error) {
	// Compute commitment
	commitment, err := kzg.BlobToCommitment(blob)
	if err != nil {
		return types.Hash{}, err
	}

	// Convert to versioned hash
	return kzg.CommitmentToVersionedHash(commitment), nil
}

// VerifyBlobHashes verifies that blob hashes match the sidecar
func VerifyBlobHashes(expectedHashes []types.Hash, sidecar *transaction.BlobTxSidecar) error {
	if sidecar == nil {
		return errors.New("sidecar is nil")
	}

	if len(expectedHashes) != len(sidecar.Blobs) {
		return errors.New("hash count mismatch")
	}

	for i, blob := range sidecar.Blobs {
		hash, err := ComputeBlobHash(&blob)
		if err != nil {
			return err
		}
		if hash != expectedHashes[i] {
			return errors.New("blob hash mismatch")
		}
	}

	return nil
}

// =============================================================================
// EIP-4844 Header Fields
// =============================================================================

// BlobGasUsed returns the blob gas used by transactions in a block
func BlobGasUsed(txs []*transaction.Transaction) uint64 {
	var total uint64
	for _, tx := range txs {
		if tx.Type() == transaction.BlobTxType {
			// Each blob consumes BlobTxBlobGasPerBlob gas
			// The number of blobs can be inferred from BlobHashes
			blobHashes := tx.BlobHashes()
			if blobHashes != nil {
				total += uint64(len(blobHashes)) * transaction.BlobTxBlobGasPerBlob
			}
		}
	}
	return total
}

// ValidateBlobGasUsed validates the blob gas used field in a block header
func ValidateBlobGasUsed(blobGasUsed uint64, txs []*transaction.Transaction) error {
	expected := BlobGasUsed(txs)
	if blobGasUsed != expected {
		return errors.New("invalid blob gas used")
	}
	if blobGasUsed > transaction.MaxBlobGasPerBlock {
		return errors.New("blob gas exceeds maximum")
	}
	return nil
}

// =============================================================================
// Fake/Mock Blob Functions (for testing)
// =============================================================================

// CreateMockBlob creates a mock blob for testing
func CreateMockBlob(data []byte) transaction.Blob {
	var blob transaction.Blob
	copy(blob[:], data)
	return blob
}

// CreateMockCommitment creates a mock commitment for testing
func CreateMockCommitment(blob *transaction.Blob) transaction.Commitment {
	h := sha256.Sum256(blob[:])
	var commitment transaction.Commitment
	copy(commitment[:], h[:])
	return commitment
}

// CreateMockProof creates a mock proof for testing
func CreateMockProof() transaction.Proof {
	return transaction.Proof{}
}

// CreateMockSidecar creates a mock sidecar for testing
func CreateMockSidecar(numBlobs int) *transaction.BlobTxSidecar {
	sidecar := &transaction.BlobTxSidecar{
		Blobs:       make([]transaction.Blob, numBlobs),
		Commitments: make([]transaction.Commitment, numBlobs),
		Proofs:      make([]transaction.Proof, numBlobs),
	}

	for i := 0; i < numBlobs; i++ {
		sidecar.Commitments[i] = CreateMockCommitment(&sidecar.Blobs[i])
	}

	return sidecar
}

