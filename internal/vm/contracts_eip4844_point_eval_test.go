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

package vm

import (
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/kzg"
)

// vmTBuildPointEvalInput builds a real, valid 192-byte point-evaluation
// precompile input using the actual go-kzg-4844-backed trusted setup: an
// all-zero blob, its commitment, a proof at evaluation point 1, and the
// versioned hash derived from the commitment.
func vmTBuildPointEvalInput(t *testing.T) []byte {
	t.Helper()
	if err := kzg.InitContext(); err != nil {
		t.Fatalf("kzg.InitContext failed: %v", err)
	}

	var blob transaction.Blob
	commitment, err := kzg.BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment failed: %v", err)
	}

	point := [32]byte{}
	point[31] = 0x01

	proof, claim, err := kzg.ComputeProof(&blob, commitment, point)
	if err != nil {
		t.Fatalf("ComputeProof failed: %v", err)
	}

	versionedHash := kzg.CommitmentToVersionedHash(commitment)

	input := make([]byte, 192)
	copy(input[0:32], versionedHash[:])
	copy(input[32:64], point[:])
	copy(input[64:96], claim[:])
	copy(input[96:144], commitment[:])
	copy(input[144:192], proof[:])
	return input
}

// TestVMTPointEvaluationPrecompileValid exercises the full happy path of
// the EIP-4844 point evaluation precompile with a real KZG proof.
func TestVMTPointEvaluationPrecompileValid(t *testing.T) {
	input := vmTBuildPointEvalInput(t)

	pc := GetPointEvaluationPrecompile()
	if gas := pc.RequiredGas(input); gas == 0 {
		t.Fatalf("expected nonzero RequiredGas")
	}

	out, err := pc.Run(input)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(out) != 64 {
		t.Fatalf("expected 64-byte output, got %d", len(out))
	}
	if out[31] != 0x00 || out[30] != 0x10 { // FIELD_ELEMENTS_PER_BLOB = 4096 = 0x1000
		t.Fatalf("unexpected FIELD_ELEMENTS_PER_BLOB encoding: %x", out[:32])
	}
}

// TestVMTPointEvaluationPrecompileBadLength verifies the input-length guard.
func TestVMTPointEvaluationPrecompileBadLength(t *testing.T) {
	pc := GetPointEvaluationPrecompile()
	if _, err := pc.Run(make([]byte, 191)); err == nil {
		t.Fatalf("expected an error for a too-short input")
	}
}

// TestVMTPointEvaluationPrecompileBadVersionByte verifies
// verifyVersionedHash's version-byte check.
func TestVMTPointEvaluationPrecompileBadVersionByte(t *testing.T) {
	input := vmTBuildPointEvalInput(t)
	input[0] = 0xff // corrupt the version byte

	pc := GetPointEvaluationPrecompile()
	if _, err := pc.Run(input); err == nil {
		t.Fatalf("expected an error for a bad versioned-hash version byte")
	}
}

// TestVMTPointEvaluationPrecompileHashMismatch verifies the
// versioned-hash-vs-commitment mismatch branch.
func TestVMTPointEvaluationPrecompileHashMismatch(t *testing.T) {
	input := vmTBuildPointEvalInput(t)
	input[31] ^= 0xff // corrupt a hash byte while keeping the version byte

	pc := GetPointEvaluationPrecompile()
	if _, err := pc.Run(input); err == nil {
		t.Fatalf("expected an error for a mismatched versioned hash")
	}
}

// TestVMTPointEvaluationPrecompileBadProof verifies the KZG proof
// verification failure branch.
func TestVMTPointEvaluationPrecompileBadProof(t *testing.T) {
	input := vmTBuildPointEvalInput(t)
	input[150] ^= 0xff // corrupt a proof byte

	pc := GetPointEvaluationPrecompile()
	if _, err := pc.Run(input); err == nil {
		t.Fatalf("expected an error for an invalid KZG proof")
	}
}

// TestVMTBlobHelpers covers ComputeBlobHash, VerifyBlobHashes, BlobGasUsed,
// ValidateBlobGasUsed, and the CreateMock* test helpers.
func TestVMTBlobHelpers(t *testing.T) {
	if err := kzg.InitContext(); err != nil {
		t.Fatalf("kzg.InitContext failed: %v", err)
	}

	blob := CreateMockBlob([]byte("hello blob"))
	hash, err := ComputeBlobHash(&blob)
	if err != nil {
		t.Fatalf("ComputeBlobHash failed: %v", err)
	}
	if hash.String() == (types.Hash{}).String() {
		t.Fatalf("expected a non-zero blob hash")
	}

	sidecar := CreateMockSidecar(1)
	sidecar.Blobs[0] = blob
	commitment := CreateMockCommitment(&blob)
	sidecar.Commitments[0] = commitment
	sidecar.Proofs[0] = CreateMockProof()

	expectedHash, err := ComputeBlobHash(&blob)
	if err != nil {
		t.Fatalf("ComputeBlobHash failed: %v", err)
	}
	if err := VerifyBlobHashes([]types.Hash{expectedHash}, sidecar); err != nil {
		t.Fatalf("VerifyBlobHashes failed: %v", err)
	}

	// Mismatch case.
	if err := VerifyBlobHashes([]types.Hash{{}}, sidecar); err == nil {
		t.Fatalf("expected VerifyBlobHashes to fail on a hash mismatch")
	}

	// Nil sidecar case.
	if err := VerifyBlobHashes(nil, nil); err == nil {
		t.Fatalf("expected VerifyBlobHashes to fail on a nil sidecar")
	}

	if got := BlobGasUsed(nil); got != 0 {
		t.Fatalf("expected 0 blob gas for no transactions, got %d", got)
	}
	if err := ValidateBlobGasUsed(0, nil); err != nil {
		t.Fatalf("ValidateBlobGasUsed(0, nil) should succeed, got %v", err)
	}
	if err := ValidateBlobGasUsed(1, nil); err == nil {
		t.Fatalf("expected ValidateBlobGasUsed to reject a mismatched blobGasUsed")
	}
}
