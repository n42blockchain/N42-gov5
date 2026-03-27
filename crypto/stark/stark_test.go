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

package stark

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func generateTestInputs(count int) []SignatureInput {
	inputs := make([]SignatureInput, count)
	for i := 0; i < count; i++ {
		pubKey := make([]byte, 897) // Falcon-512 size
		sig := make([]byte, 666)
		rand.Read(pubKey)
		rand.Read(sig)
		inputs[i] = SignatureInput{
			PublicKey: pubKey,
			Signature: sig,
			Algorithm: 0, // Falcon
		}
	}
	return inputs
}

func TestProverAggregateSignatures(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	message := []byte("test message to sign")
	inputs := generateTestInputs(10)

	proof, err := prover.AggregateSignatures(message, inputs)
	if err != nil {
		t.Fatalf("AggregateSignatures failed: %v", err)
	}

	if proof.Version != ProofVersion {
		t.Errorf("Version = %d, want %d", proof.Version, ProofVersion)
	}
	if proof.SignerCount != 10 {
		t.Errorf("SignerCount = %d, want 10", proof.SignerCount)
	}
	if proof.PublicKeyRoot == (types.Hash{}) {
		t.Error("PublicKeyRoot should not be zero")
	}
	if proof.SignatureRoot == (types.Hash{}) {
		t.Error("SignatureRoot should not be zero")
	}
	if proof.AggregateHash == (types.Hash{}) {
		t.Error("AggregateHash should not be zero")
	}
}

func TestProverEmptyMessage(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	inputs := generateTestInputs(5)

	_, err := prover.AggregateSignatures([]byte{}, inputs)
	if err != ErrEmptyMessage {
		t.Errorf("Expected ErrEmptyMessage, got %v", err)
	}
}

func TestProverTooFewSignatures(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	message := []byte("test")

	_, err := prover.AggregateSignatures(message, []SignatureInput{})
	if err != ErrTooFewSignatures {
		t.Errorf("Expected ErrTooFewSignatures, got %v", err)
	}
}

func TestProofSerialization(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	message := []byte("test message")
	inputs := generateTestInputs(5)

	proof, err := prover.AggregateSignatures(message, inputs)
	if err != nil {
		t.Fatalf("AggregateSignatures failed: %v", err)
	}

	// Serialize
	data := proof.Bytes()
	if len(data) == 0 {
		t.Fatal("Serialized proof should not be empty")
	}

	// Deserialize
	parsed, err := ParseProof(data)
	if err != nil {
		t.Fatalf("ParseProof failed: %v", err)
	}

	// Compare
	if parsed.Version != proof.Version {
		t.Errorf("Version mismatch: %d vs %d", parsed.Version, proof.Version)
	}
	if parsed.SignerCount != proof.SignerCount {
		t.Errorf("SignerCount mismatch: %d vs %d", parsed.SignerCount, proof.SignerCount)
	}
	if parsed.Message != proof.Message {
		t.Error("Message mismatch")
	}
	if parsed.PublicKeyRoot != proof.PublicKeyRoot {
		t.Error("PublicKeyRoot mismatch")
	}
	if parsed.SignatureRoot != proof.SignatureRoot {
		t.Error("SignatureRoot mismatch")
	}
	if parsed.AggregateHash != proof.AggregateHash {
		t.Error("AggregateHash mismatch")
	}
}

func TestVerifier(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	verifier := NewVerifier()
	message := []byte("test message")
	inputs := generateTestInputs(10)

	proof, err := prover.AggregateSignatures(message, inputs)
	if err != nil {
		t.Fatalf("AggregateSignatures failed: %v", err)
	}

	// Build commitments
	commitments := make([]types.Hash, len(inputs))
	for i, input := range inputs {
		commitments[i] = input.PublicKeyHash()
	}

	// Verify
	err = verifier.Verify(proof, commitments)
	if err != nil {
		t.Errorf("Verification failed: %v", err)
	}
}

func TestVerifierWithMessage(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	verifier := NewVerifier()
	message := []byte("test message")
	inputs := generateTestInputs(10)

	proof, err := prover.AggregateSignatures(message, inputs)
	if err != nil {
		t.Fatalf("AggregateSignatures failed: %v", err)
	}

	// Full verification
	err = verifier.VerifyWithMessage(proof, message, inputs)
	if err != nil {
		t.Errorf("VerifyWithMessage failed: %v", err)
	}
}

func TestVerifierInvalidCommitments(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	verifier := NewVerifier()
	message := []byte("test message")
	inputs := generateTestInputs(5)

	proof, _ := prover.AggregateSignatures(message, inputs)

	// Wrong commitments
	wrongCommitments := make([]types.Hash, len(inputs))
	for i := range wrongCommitments {
		rand.Read(wrongCommitments[i][:])
	}

	err := verifier.Verify(proof, wrongCommitments)
	if err != ErrVerificationFailed {
		t.Errorf("Expected ErrVerificationFailed, got %v", err)
	}
}

func TestVerifierWrongMessage(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	verifier := NewVerifier()
	message := []byte("test message")
	inputs := generateTestInputs(5)

	proof, _ := prover.AggregateSignatures(message, inputs)

	// Wrong message
	err := verifier.VerifyWithMessage(proof, []byte("wrong message"), inputs)
	if err != ErrVerificationFailed {
		t.Errorf("Expected ErrVerificationFailed, got %v", err)
	}
}

func TestVerifierRejectsZeroSignerProof(t *testing.T) {
	verifier := NewVerifier()
	proof := &AggregatedProof{
		Version:         ProofVersion,
		SignerCount:     0,
		AggregateHash:   computeAggregateHash(types.Hash{}, types.Hash{}, types.Hash{}, types.Hash{}),
		RandomChallenge: types.Hash{},
	}

	if err := verifier.Verify(proof, nil); err != ErrInvalidProof {
		t.Fatalf("Verify() error = %v, want %v", err, ErrInvalidProof)
	}
	if err := verifier.VerifyWithMessage(proof, []byte("msg"), nil); err != ErrInvalidProof {
		t.Fatalf("VerifyWithMessage() error = %v, want %v", err, ErrInvalidProof)
	}
}

func TestParseProofRejectsInvalidSignerCount(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	proof, err := prover.AggregateSignatures([]byte("test"), generateTestInputs(2))
	if err != nil {
		t.Fatal(err)
	}

	encoded := proof.Bytes()

	binary.BigEndian.PutUint32(encoded[1:5], 0)
	if _, err := ParseProof(encoded); err != ErrInvalidProof {
		t.Fatalf("ParseProof(zero signer count) error = %v, want %v", err, ErrInvalidProof)
	}

	encoded = proof.Bytes()
	binary.BigEndian.PutUint32(encoded[1:5], MaxSignatures+1)
	if _, err := ParseProof(encoded); err != ErrInvalidProof {
		t.Fatalf("ParseProof(too many signers) error = %v, want %v", err, ErrInvalidProof)
	}
}

func TestMerkleRoot(t *testing.T) {
	// Single hash
	h1 := types.Hash{1, 2, 3}
	root1 := computeMerkleRoot([]types.Hash{h1})
	if root1 != h1 {
		t.Error("Single hash should return itself as root")
	}

	// Two hashes
	h2 := types.Hash{4, 5, 6}
	root2 := computeMerkleRoot([]types.Hash{h1, h2})
	if root2 == (types.Hash{}) {
		t.Error("Root of two hashes should not be zero")
	}

	// Deterministic
	root2Again := computeMerkleRoot([]types.Hash{h1, h2})
	if root2 != root2Again {
		t.Error("Merkle root should be deterministic")
	}
}

func TestSignerBitmap(t *testing.T) {
	bitmap := createSignerBitmap(10)

	// Check all signers are set
	for i := 0; i < 10; i++ {
		if !GetSignerFromBitmap(bitmap, i) {
			t.Errorf("Signer %d should be set", i)
		}
	}

	// Check beyond range is not set
	if GetSignerFromBitmap(bitmap, 100) {
		t.Error("Index 100 should not be set")
	}

	// Count
	count := CountSignersInBitmap(bitmap)
	if count != 10 {
		t.Errorf("Count = %d, want 10", count)
	}
}

func TestEstimateProofSize(t *testing.T) {
	sizes := []struct {
		count   int
		merkle  bool
		minSize int
	}{
		{10, false, 100},
		{100, false, 100},
		{10, true, 100},
	}

	for _, s := range sizes {
		size := EstimateProofSize(s.count, s.merkle)
		if size < s.minSize {
			t.Errorf("EstimateProofSize(%d, %v) = %d, should be at least %d",
				s.count, s.merkle, size, s.minSize)
		}
		t.Logf("Proof size for %d signers (merkle=%v): %d bytes", s.count, s.merkle, size)
	}
}

func TestSignatureInputHash(t *testing.T) {
	input := SignatureInput{
		PublicKey: []byte("test public key"),
		Signature: []byte("test signature"),
		Algorithm: 0,
	}

	h1 := input.Hash()
	h2 := input.Hash()

	if h1 != h2 {
		t.Error("Hash should be deterministic")
	}
	if h1 == (types.Hash{}) {
		t.Error("Hash should not be zero")
	}
}

func TestNextPowerOf2(t *testing.T) {
	tests := []struct {
		input    int
		expected int
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{3, 4},
		{4, 4},
		{5, 8},
		{7, 8},
		{8, 8},
		{9, 16},
		{100, 128},
	}

	for _, tt := range tests {
		got := nextPowerOf2(tt.input)
		if got != tt.expected {
			t.Errorf("nextPowerOf2(%d) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestProofSize(t *testing.T) {
	prover := NewProver(DefaultProverConfig())
	message := []byte("test")

	tests := []int{1, 10, 50, 100}
	for _, count := range tests {
		inputs := generateTestInputs(count)
		proof, _ := prover.AggregateSignatures(message, inputs)

		serialized := proof.Bytes()
		calculatedSize := proof.Size()

		if len(serialized) != calculatedSize {
			t.Errorf("Size mismatch for %d signers: serialized=%d, calculated=%d",
				count, len(serialized), calculatedSize)
		}

		t.Logf("Proof for %d signers: %d bytes", count, len(serialized))
	}
}

// Benchmarks

func BenchmarkAggregateSignatures10(b *testing.B) {
	prover := NewProver(DefaultProverConfig())
	message := []byte("benchmark message")
	inputs := generateTestInputs(10)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prover.AggregateSignatures(message, inputs)
	}
}

func BenchmarkAggregateSignatures100(b *testing.B) {
	prover := NewProver(DefaultProverConfig())
	message := []byte("benchmark message")
	inputs := generateTestInputs(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prover.AggregateSignatures(message, inputs)
	}
}

func BenchmarkVerify100(b *testing.B) {
	prover := NewProver(DefaultProverConfig())
	verifier := NewVerifier()
	message := []byte("benchmark message")
	inputs := generateTestInputs(100)

	proof, _ := prover.AggregateSignatures(message, inputs)
	commitments := make([]types.Hash, len(inputs))
	for i, input := range inputs {
		commitments[i] = input.PublicKeyHash()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		verifier.Verify(proof, commitments)
	}
}

func BenchmarkProofSerialization(b *testing.B) {
	prover := NewProver(DefaultProverConfig())
	message := []byte("benchmark message")
	inputs := generateTestInputs(100)
	proof, _ := prover.AggregateSignatures(message, inputs)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data := proof.Bytes()
		ParseProof(data)
		_ = data
	}
}

func BenchmarkMerkleRoot100(b *testing.B) {
	hashes := make([]types.Hash, 100)
	for i := range hashes {
		rand.Read(hashes[i][:])
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeMerkleRoot(hashes)
	}
}

func TestLargeAggregation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large aggregation test in short mode")
	}

	prover := NewProver(DefaultProverConfig())
	verifier := NewVerifier()
	message := []byte("large aggregation test")

	counts := []int{100, 500, 1000}
	for _, count := range counts {
		inputs := generateTestInputs(count)
		proof, err := prover.AggregateSignatures(message, inputs)
		if err != nil {
			t.Errorf("Failed to aggregate %d signatures: %v", count, err)
			continue
		}

		commitments := make([]types.Hash, len(inputs))
		for i, input := range inputs {
			commitments[i] = input.PublicKeyHash()
		}

		err = verifier.Verify(proof, commitments)
		if err != nil {
			t.Errorf("Failed to verify %d signatures: %v", count, err)
		}

		t.Logf("Aggregated %d signatures: proof size = %d bytes", count, proof.Size())
	}
}

func TestProofDeterminism(t *testing.T) {
	// Note: Proofs are NOT deterministic due to random challenge
	// But the structure should be consistent
	prover := NewProver(DefaultProverConfig())
	message := []byte("test")
	inputs := generateTestInputs(5)

	proof1, _ := prover.AggregateSignatures(message, inputs)
	proof2, _ := prover.AggregateSignatures(message, inputs)

	// These should match (derived from inputs)
	if proof1.Message != proof2.Message {
		t.Error("Message should match")
	}
	if proof1.PublicKeyRoot != proof2.PublicKeyRoot {
		t.Error("PublicKeyRoot should match")
	}
	if proof1.SignatureRoot != proof2.SignatureRoot {
		t.Error("SignatureRoot should match")
	}
	if !bytes.Equal(proof1.SignerBitmap, proof2.SignerBitmap) {
		t.Error("SignerBitmap should match")
	}

	// These should differ (random challenge)
	if proof1.RandomChallenge == proof2.RandomChallenge {
		t.Log("Warning: RandomChallenge matched (unlikely but possible)")
	}
}
