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
	// BlobCommitmentVersionKZG should be 0x01
	if BlobCommitmentVersionKZG != 0x01 {
		t.Errorf("BlobCommitmentVersionKZG: expected 0x01, got 0x%02x", BlobCommitmentVersionKZG)
	}

	// FieldElementsPerBlob should be 4096
	if FieldElementsPerBlob != 4096 {
		t.Errorf("FieldElementsPerBlob: expected 4096, got %d", FieldElementsPerBlob)
	}

	// BytesPerFieldElement should be 32
	if BytesPerFieldElement != 32 {
		t.Errorf("BytesPerFieldElement: expected 32, got %d", BytesPerFieldElement)
	}

	// BytesPerBlob should be 131072 (4096 * 32)
	if BytesPerBlob != 131072 {
		t.Errorf("BytesPerBlob: expected 131072, got %d", BytesPerBlob)
	}

	// BytesPerCommitment should be 48
	if BytesPerCommitment != 48 {
		t.Errorf("BytesPerCommitment: expected 48, got %d", BytesPerCommitment)
	}

	// BytesPerProof should be 48
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
	if !ctx.initialized {
		t.Error("Context not initialized")
	}
}

// =============================================================================
// Versioned Hash Tests
// =============================================================================

func TestCommitmentToVersionedHash(t *testing.T) {
	// Create a test commitment
	var commitment Commitment
	for i := range commitment {
		commitment[i] = byte(i)
	}

	hash := CommitmentToVersionedHash(commitment)

	// First byte should be version
	if hash[0] != BlobCommitmentVersionKZG {
		t.Errorf("Version byte: expected 0x%02x, got 0x%02x", BlobCommitmentVersionKZG, hash[0])
	}

	// Hash should be deterministic
	hash2 := CommitmentToVersionedHash(commitment)
	if hash != hash2 {
		t.Error("CommitmentToVersionedHash should be deterministic")
	}

	// Different commitments should produce different hashes
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
	// Empty blob should be valid
	var blob Blob
	if err := ValidateBlob(&blob); err != nil {
		t.Errorf("ValidateBlob(empty) error: %v", err)
	}

	// Blob with random data should be valid
	for i := range blob {
		blob[i] = byte(i % 256)
	}
	if err := ValidateBlob(&blob); err != nil {
		t.Errorf("ValidateBlob(random) error: %v", err)
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
// KZG Operations Tests
// =============================================================================

func TestBlobToCommitment(t *testing.T) {
	// Initialize context
	if err := InitContext(); err != nil {
		t.Fatalf("InitContext() error: %v", err)
	}

	var blob Blob
	for i := range blob {
		blob[i] = byte(i % 256)
	}

	commitment, err := BlobToCommitment(&blob)
	if err != nil {
		t.Errorf("BlobToCommitment() error: %v", err)
	}

	// Commitment should not be all zeros
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

func TestComputeProof(t *testing.T) {
	if err := InitContext(); err != nil {
		t.Fatalf("InitContext() error: %v", err)
	}

	var blob Blob
	commitment, _ := BlobToCommitment(&blob)
	point := [32]byte{0x01}

	proof, claim, err := ComputeProof(&blob, commitment, point)
	if err != nil {
		t.Errorf("ComputeProof() error: %v", err)
	}

	// Proof should not be all zeros
	allZero := true
	for _, b := range proof {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("Proof should not be all zeros")
	}

	// Claim should not be all zeros
	allZero = true
	for _, b := range claim {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("Claim should not be all zeros")
	}
}

func TestVerifyProof(t *testing.T) {
	if err := InitContext(); err != nil {
		t.Fatalf("InitContext() error: %v", err)
	}

	var blob Blob
	commitment, _ := BlobToCommitment(&blob)
	point := [32]byte{0x01}
	proof, claim, _ := ComputeProof(&blob, commitment, point)

	// Valid proof should verify
	err := VerifyProof(commitment, point, claim, proof)
	if err != nil {
		t.Errorf("VerifyProof() error: %v", err)
	}

	// Invalid (zero) commitment should fail
	err = VerifyProof(Commitment{}, point, claim, proof)
	if err != ErrInvalidCommitment {
		t.Errorf("Expected ErrInvalidCommitment, got %v", err)
	}
}

func TestVerifyBlobProof(t *testing.T) {
	if err := InitContext(); err != nil {
		t.Fatalf("InitContext() error: %v", err)
	}

	var blob Blob
	commitment, _ := BlobToCommitment(&blob)
	proof := Proof{}

	// Matching commitment should verify
	err := VerifyBlobProof(&blob, commitment, proof)
	if err != nil {
		t.Errorf("VerifyBlobProof() error: %v", err)
	}

	// Mismatched commitment should fail
	var commitment2 Commitment
	commitment2[0] = 0xFF
	err = VerifyBlobProof(&blob, commitment2, proof)
	if err != ErrCommitmentMismatch {
		t.Errorf("Expected ErrCommitmentMismatch, got %v", err)
	}
}

func TestVerifyBlobProofBatch(t *testing.T) {
	if err := InitContext(); err != nil {
		t.Fatalf("InitContext() error: %v", err)
	}

	blobs := make([]Blob, 3)
	commitments := make([]Commitment, 3)
	proofs := make([]Proof, 3)

	for i := range blobs {
		commitments[i], _ = BlobToCommitment(&blobs[i])
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

func BenchmarkVerifyProof(b *testing.B) {
	InitContext()
	var blob Blob
	commitment, _ := BlobToCommitment(&blob)
	point := [32]byte{0x01}
	proof, claim, _ := ComputeProof(&blob, commitment, point)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		VerifyProof(commitment, point, claim, proof)
	}
}

func BenchmarkVerifyBlobProofBatch(b *testing.B) {
	InitContext()
	blobs := make([]Blob, 6)
	commitments := make([]Commitment, 6)
	proofs := make([]Proof, 6)

	for i := range blobs {
		commitments[i], _ = BlobToCommitment(&blobs[i])
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		VerifyBlobProofBatch(blobs, commitments, proofs)
	}
}

func BenchmarkValidateBlob(b *testing.B) {
	var blob Blob
	for i := range blob {
		blob[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ValidateBlob(&blob)
	}
}

