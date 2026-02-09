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

package kzg

import (
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Constants Tests
// =============================================================================

func TestKZGConstants(t *testing.T) {
	if BlobCommitmentVersionKZG != 0x01 {
		t.Errorf("BlobCommitmentVersionKZG: expected 0x01, got 0x%02x", BlobCommitmentVersionKZG)
	}
	if FieldElementsPerBlob != 4096 {
		t.Errorf("FieldElementsPerBlob: expected 4096, got %d", FieldElementsPerBlob)
	}
	if BytesPerFieldElement != 32 {
		t.Errorf("BytesPerFieldElement: expected 32, got %d", BytesPerFieldElement)
	}
	if BytesPerBlob != 131072 {
		t.Errorf("BytesPerBlob: expected 131072, got %d", BytesPerBlob)
	}
	if BytesPerCommitment != 48 {
		t.Errorf("BytesPerCommitment: expected 48, got %d", BytesPerCommitment)
	}
	if BytesPerProof != 48 {
		t.Errorf("BytesPerProof: expected 48, got %d", BytesPerProof)
	}
}

// =============================================================================
// Context Tests
// =============================================================================

func TestInitContext(t *testing.T) {
	err := InitContext()
	if err != nil {
		t.Errorf("InitContext() error: %v", err)
	}
}

func TestGetContext(t *testing.T) {
	ctx, err := GetContext()
	if err != nil {
		t.Errorf("GetContext() error: %v", err)
	}
	if ctx == nil {
		t.Error("GetContext() returned nil")
	}
	if ctx.inner == nil {
		t.Error("Context.inner not initialized")
	}
}

// =============================================================================
// Versioned Hash Tests
// =============================================================================

func TestCommitmentToVersionedHash(t *testing.T) {
	var commitment Commitment
	for i := range commitment {
		commitment[i] = byte(i)
	}

	hash := CommitmentToVersionedHash(commitment)

	if hash[0] != BlobCommitmentVersionKZG {
		t.Errorf("Version byte: expected 0x%02x, got 0x%02x", BlobCommitmentVersionKZG, hash[0])
	}

	// Deterministic
	hash2 := CommitmentToVersionedHash(commitment)
	if hash != hash2 {
		t.Error("CommitmentToVersionedHash should be deterministic")
	}

	// Different commitments produce different hashes
	var commitment2 Commitment
	commitment2[0] = 0xFF
	hash3 := CommitmentToVersionedHash(commitment2)
	if hash == hash3 {
		t.Error("Different commitments should produce different hashes")
	}
}

func TestIsValidVersionedHash(t *testing.T) {
	tests := []struct {
		name  string
		hash  types.Hash
		valid bool
	}{
		{
			"valid KZG version",
			types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001"),
			true,
		},
		{
			"zero version",
			types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
			false,
		},
		{
			"version 2",
			types.HexToHash("0x0200000000000000000000000000000000000000000000000000000000000001"),
			false,
		},
		{
			"all zeros",
			types.Hash{},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidVersionedHash(tt.hash); got != tt.valid {
				t.Errorf("IsValidVersionedHash() = %v, want %v", got, tt.valid)
			}
		})
	}
}

// =============================================================================
// Blob Validation Tests
// =============================================================================

func TestValidateBlob(t *testing.T) {
	// Zero blob should be valid (all field elements are 0 < BLS modulus)
	var blob Blob
	if err := ValidateBlob(&blob); err != nil {
		t.Errorf("ValidateBlob(empty) error: %v", err)
	}

	// Blob with field element >= BLS modulus should fail
	var badBlob Blob
	// Set first field element to 0x73eda7... (>= BLS modulus)
	badBlob[0] = 0x73
	badBlob[1] = 0xed
	badBlob[2] = 0xa7
	badBlob[3] = 0x53
	badBlob[4] = 0x29
	badBlob[5] = 0x9d
	badBlob[6] = 0x7d
	badBlob[7] = 0x48
	badBlob[8] = 0x33
	badBlob[9] = 0x39
	badBlob[10] = 0xd8
	badBlob[11] = 0x08
	badBlob[12] = 0x09
	badBlob[13] = 0xa1
	badBlob[14] = 0xd8
	badBlob[15] = 0x05
	badBlob[16] = 0x53
	badBlob[17] = 0xbd
	badBlob[18] = 0xa4
	badBlob[19] = 0x02
	badBlob[20] = 0xff
	badBlob[21] = 0xfe
	badBlob[22] = 0x5b
	badBlob[23] = 0xfe
	badBlob[24] = 0xff
	badBlob[25] = 0xff
	badBlob[26] = 0xff
	badBlob[27] = 0xff
	badBlob[28] = 0x00
	badBlob[29] = 0x00
	badBlob[30] = 0x00
	badBlob[31] = 0x01 // This is exactly BLS modulus, which is non-canonical
	if err := ValidateBlob(&badBlob); err == nil {
		t.Error("ValidateBlob should reject blob with non-canonical field element")
	}
}

func TestValidateBlobSidecar(t *testing.T) {
	// Nil sidecar should fail
	err := ValidateBlobSidecar(nil, nil)
	if err != ErrSidecarMissing {
		t.Errorf("Expected ErrSidecarMissing, got %v", err)
	}

	// Empty sidecar should fail
	sidecar := &transaction.BlobTxSidecar{}
	err = ValidateBlobSidecar(sidecar, nil)
	if err != ErrNoBlobs {
		t.Errorf("Expected ErrNoBlobs, got %v", err)
	}

	// Mismatched counts should fail
	sidecar = &transaction.BlobTxSidecar{
		Blobs:       make([]Blob, 2),
		Commitments: make([]Commitment, 1),
		Proofs:      make([]Proof, 2),
	}
	err = ValidateBlobSidecar(sidecar, []types.Hash{{}, {}})
	if err != ErrCommitmentCountMismatch {
		t.Errorf("Expected ErrCommitmentCountMismatch, got %v", err)
	}
}

// =============================================================================
// KZG Operations Tests (end-to-end with real trusted setup)
// =============================================================================

func TestBlobToCommitment(t *testing.T) {
	if err := InitContext(); err != nil {
		t.Fatalf("InitContext() error: %v", err)
	}

	var blob Blob
	commitment, err := BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment() error: %v", err)
	}

	// Commitment should not be all zeros (even for zero blob, it's a valid G1 point)
	allZero := true
	for _, b := range commitment {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("Commitment should not be all zeros")
	}

	// Same blob should produce same commitment
	commitment2, _ := BlobToCommitment(&blob)
	if commitment != commitment2 {
		t.Error("Same blob should produce same commitment")
	}
}

func TestComputeAndVerifyProof(t *testing.T) {
	if err := InitContext(); err != nil {
		t.Fatalf("InitContext() error: %v", err)
	}

	var blob Blob
	commitment, err := BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment() error: %v", err)
	}

	// Use a valid scalar as evaluation point (1)
	point := [32]byte{}
	point[31] = 0x01

	proof, claim, err := ComputeProof(&blob, commitment, point)
	if err != nil {
		t.Fatalf("ComputeProof() error: %v", err)
	}

	// Verify the proof
	err = VerifyProof(commitment, point, claim, proof)
	if err != nil {
		t.Errorf("VerifyProof() should succeed for valid proof, got: %v", err)
	}
}

func TestComputeAndVerifyBlobProof(t *testing.T) {
	if err := InitContext(); err != nil {
		t.Fatalf("InitContext() error: %v", err)
	}

	var blob Blob
	commitment, err := BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment() error: %v", err)
	}

	ctx, _ := GetContext()
	kzgProof, err := ctx.ComputeBlobProof(&blob, commitment)
	if err != nil {
		t.Fatalf("ComputeBlobProof() error: %v", err)
	}

	// Verify blob proof
	err = VerifyBlobProof(&blob, commitment, kzgProof)
	if err != nil {
		t.Errorf("VerifyBlobProof() should succeed for valid proof, got: %v", err)
	}
}

func TestVerifyBlobProofBatch(t *testing.T) {
	if err := InitContext(); err != nil {
		t.Fatalf("InitContext() error: %v", err)
	}

	ctx, _ := GetContext()
	blobs := make([]Blob, 3)
	commitments := make([]Commitment, 3)
	proofs := make([]Proof, 3)

	for i := range blobs {
		var err error
		commitments[i], err = BlobToCommitment(&blobs[i])
		if err != nil {
			t.Fatalf("BlobToCommitment() error: %v", err)
		}
		proofs[i], err = ctx.ComputeBlobProof(&blobs[i], commitments[i])
		if err != nil {
			t.Fatalf("ComputeBlobProof() error: %v", err)
		}
	}

	// Valid batch should verify
	err := VerifyBlobProofBatch(blobs, commitments, proofs)
	if err != nil {
		t.Errorf("VerifyBlobProofBatch() error: %v", err)
	}

	// Mismatched lengths should fail
	err = VerifyBlobProofBatch(blobs, commitments[:2], proofs)
	if err != ErrInputLengthMismatch {
		t.Errorf("Expected ErrInputLengthMismatch, got %v", err)
	}
}

// =============================================================================
// Error Tests
// =============================================================================

func TestKZGErrors(t *testing.T) {
	errors := []error{
		ErrContextNotInitialized,
		ErrInvalidCommitment,
		ErrCommitmentMismatch,
		ErrInputLengthMismatch,
		ErrSidecarMissing,
		ErrNoBlobs,
		ErrCommitmentCountMismatch,
		ErrProofCountMismatch,
		ErrHashCountMismatch,
		ErrVersionedHashMismatch,
		ErrInvalidProof,
		ErrFieldElementOverflow,
	}

	for _, err := range errors {
		if err.Error() == "" {
			t.Error("Error should have non-empty message")
		}
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkCommitmentToVersionedHash(b *testing.B) {
	var commitment Commitment
	for i := range commitment {
		commitment[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CommitmentToVersionedHash(commitment)
	}
}

func BenchmarkBlobToCommitment(b *testing.B) {
	InitContext()
	var blob Blob

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BlobToCommitment(&blob)
	}
}

func BenchmarkValidateBlob(b *testing.B) {
	var blob Blob

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ValidateBlob(&blob)
	}
}
