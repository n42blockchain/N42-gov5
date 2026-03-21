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
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/zkprover"
)

// makeValidProof constructs a well-formed ZKMLProof for testing.
func makeValidProof() *zkprover.ZKMLProof {
	modelHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	inputHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	outputHash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")

	publicInputs := make([]byte, 96)
	copy(publicInputs[0:32], modelHash[:])
	copy(publicInputs[32:64], inputHash[:])
	copy(publicInputs[64:96], outputHash[:])

	// Non-zero proof data (256 bytes).
	proofData := make([]byte, 256)
	for i := range proofData {
		proofData[i] = byte(i%255 + 1)
	}

	return &zkprover.ZKMLProof{
		ModelHash:    modelHash,
		InputHash:    inputHash,
		OutputHash:   outputHash,
		ProofData:    proofData,
		PublicInputs: publicInputs,
		ProofType:    zkprover.ZKMLProofTypeGroth16,
	}
}

func TestZKMLVerifier_Valid(t *testing.T) {
	v := NewZKMLVerifier()

	proof := makeValidProof()
	valid, err := v.VerifyZKMLProof(proof)
	if err != nil {
		t.Fatalf("VerifyZKMLProof: %v", err)
	}
	if !valid {
		t.Fatal("expected valid proof")
	}

	verified, failed := v.Stats()
	if verified != 1 {
		t.Fatalf("verified = %d, want 1", verified)
	}
	if failed != 0 {
		t.Fatalf("failed = %d, want 0", failed)
	}
}

func TestZKMLVerifier_InvalidProof(t *testing.T) {
	v := NewZKMLVerifier()

	// Nil proof.
	_, err := v.VerifyZKMLProof(nil)
	if !errors.Is(err, ErrZKMLProofNil) {
		t.Fatalf("nil proof: expected ErrZKMLProofNil, got %v", err)
	}

	// Empty proof data.
	proof := makeValidProof()
	proof.ProofData = nil
	_, err = v.VerifyZKMLProof(proof)
	if !errors.Is(err, ErrZKMLProofDataEmpty) {
		t.Fatalf("empty data: expected ErrZKMLProofDataEmpty, got %v", err)
	}

	// Wrong public inputs length.
	proof2 := makeValidProof()
	proof2.PublicInputs = []byte("short")
	_, err = v.VerifyZKMLProof(proof2)
	if !errors.Is(err, ErrZKMLPublicInputsLength) {
		t.Fatalf("wrong length: expected ErrZKMLPublicInputsLength, got %v", err)
	}

	// Model hash mismatch.
	proof3 := makeValidProof()
	proof3.ModelHash = types.HexToHash("0xFFFF")
	_, err = v.VerifyZKMLProof(proof3)
	if !errors.Is(err, ErrZKMLModelHashMismatch) {
		t.Fatalf("model mismatch: expected ErrZKMLModelHashMismatch, got %v", err)
	}

	// Proof data starts with zero hash.
	proof4 := makeValidProof()
	proof4.ProofData = make([]byte, 256) // all zeros
	_, err = v.VerifyZKMLProof(proof4)
	if !errors.Is(err, ErrZKMLIntegrityCheckFailed) {
		t.Fatalf("zero prefix: expected ErrZKMLIntegrityCheckFailed, got %v", err)
	}
}

func TestZKMLVerifier_Stats(t *testing.T) {
	v := NewZKMLVerifier()

	// 2 valid verifications.
	v.VerifyZKMLProof(makeValidProof())
	v.VerifyZKMLProof(makeValidProof())

	// 3 failed verifications.
	v.VerifyZKMLProof(nil)
	v.VerifyZKMLProof(nil)
	v.VerifyZKMLProof(nil)

	verified, failed := v.Stats()
	if verified != 2 {
		t.Fatalf("verified = %d, want 2", verified)
	}
	if failed != 3 {
		t.Fatalf("failed = %d, want 3", failed)
	}
}

func TestZKMLVerifier_WrongPublicInputLength(t *testing.T) {
	v := NewZKMLVerifier()

	proof := makeValidProof()
	// Set public inputs to wrong length (not 96 bytes).
	proof.PublicInputs = []byte("too short")

	valid, err := v.VerifyZKMLProof(proof)
	if valid {
		t.Fatal("expected invalid proof with wrong public input length")
	}
	if !errors.Is(err, ErrZKMLPublicInputsLength) {
		t.Fatalf("expected ErrZKMLPublicInputsLength, got %v", err)
	}

	// Also test with an empty public inputs slice.
	proof2 := makeValidProof()
	proof2.PublicInputs = nil

	valid2, err2 := v.VerifyZKMLProof(proof2)
	if valid2 {
		t.Fatal("expected invalid proof with nil public inputs")
	}
	if !errors.Is(err2, ErrZKMLPublicInputsLength) {
		t.Fatalf("expected ErrZKMLPublicInputsLength for nil, got %v", err2)
	}

	// Test with a longer-than-expected public inputs slice.
	proof3 := makeValidProof()
	proof3.PublicInputs = make([]byte, 128)
	valid3, err3 := v.VerifyZKMLProof(proof3)
	if valid3 {
		t.Fatal("expected invalid proof with 128-byte public inputs")
	}
	if !errors.Is(err3, ErrZKMLPublicInputsLength) {
		t.Fatalf("expected ErrZKMLPublicInputsLength for 128 bytes, got %v", err3)
	}

	// All three should have incremented the failed counter.
	_, failed := v.Stats()
	if failed != 3 {
		t.Fatalf("failed = %d, want 3", failed)
	}
}

func TestZKMLVerifier_ConcurrentVerification(t *testing.T) {
	v := NewZKMLVerifier()

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			proof := makeValidProof()
			valid, err := v.VerifyZKMLProof(proof)
			if err != nil {
				t.Errorf("concurrent VerifyZKMLProof: %v", err)
			}
			if !valid {
				t.Errorf("expected valid proof in concurrent verification")
			}
		}()
	}

	wg.Wait()

	verified, failed := v.Stats()
	if verified != goroutines {
		t.Fatalf("verified = %d, want %d", verified, goroutines)
	}
	if failed != 0 {
		t.Fatalf("failed = %d, want 0", failed)
	}
}
