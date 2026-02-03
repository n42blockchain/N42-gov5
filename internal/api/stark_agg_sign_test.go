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

package api

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func generateTestValidatorSignatures(count int) []*ValidatorSignature {
	sigs := make([]*ValidatorSignature, count)
	for i := 0; i < count; i++ {
		pubKey := make([]byte, 897) // Falcon-512 size
		sig := make([]byte, 666)
		rand.Read(pubKey)
		rand.Read(sig)
		sigs[i] = &ValidatorSignature{
			ValidatorIndex: uint64(i),
			PublicKey:      pubKey,
			Signature:      sig,
			Algorithm:      0, // Falcon
		}
	}
	return sigs
}

func TestSTARKAggregator(t *testing.T) {
	aggregator := NewSTARKAggregator(DefaultSTARKAggregatorConfig())
	message := []byte("test block hash")
	signatures := generateTestValidatorSignatures(10)

	// Aggregate
	aggSig, err := aggregator.Aggregate(message, signatures)
	if err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	if aggSig.SignerCount != 10 {
		t.Errorf("SignerCount = %d, want 10", aggSig.SignerCount)
	}
	if len(aggSig.Commitments) != 10 {
		t.Errorf("Commitments count = %d, want 10", len(aggSig.Commitments))
	}
	if len(aggSig.Proof) == 0 {
		t.Error("Proof should not be empty")
	}

	// Verify
	err = aggregator.Verify(aggSig)
	if err != nil {
		t.Errorf("Verify failed: %v", err)
	}

	t.Logf("Aggregated %d signatures into %d byte proof", len(signatures), len(aggSig.Proof))
}

func TestSTARKAggregatorVerifyWithMessage(t *testing.T) {
	aggregator := NewSTARKAggregator(DefaultSTARKAggregatorConfig())
	message := []byte("test message")
	signatures := generateTestValidatorSignatures(5)

	aggSig, err := aggregator.Aggregate(message, signatures)
	if err != nil {
		t.Fatalf("Aggregate failed: %v", err)
	}

	err = aggregator.VerifyWithMessage(aggSig, message, signatures)
	if err != nil {
		t.Errorf("VerifyWithMessage failed: %v", err)
	}
}

func TestSTARKAggregatorNoSignatures(t *testing.T) {
	aggregator := NewSTARKAggregator(DefaultSTARKAggregatorConfig())
	message := []byte("test")

	_, err := aggregator.Aggregate(message, []*ValidatorSignature{})
	if err != ErrNoSignatures {
		t.Errorf("Expected ErrNoSignatures, got %v", err)
	}
}

func TestSignatureCollector(t *testing.T) {
	message := types.Hash{1, 2, 3}
	collector := NewSignatureCollector(message, 3)

	// Add signatures
	for i := 0; i < 3; i++ {
		sig := &ValidatorSignature{
			ValidatorIndex: uint64(i),
			PublicKey:      make([]byte, 100),
			Signature:      make([]byte, 100),
			Algorithm:      0,
		}
		err := collector.AddSignature(sig)
		if err != nil {
			t.Errorf("AddSignature failed: %v", err)
		}
	}

	if collector.Count() != 3 {
		t.Errorf("Count = %d, want 3", collector.Count())
	}

	sigs := collector.GetSignatures()
	if len(sigs) != 3 {
		t.Errorf("GetSignatures count = %d, want 3", len(sigs))
	}
}

func TestSignatureCollectorWait(t *testing.T) {
	message := types.Hash{1, 2, 3}
	collector := NewSignatureCollector(message, 2)

	// Start waiting in goroutine
	done := make(chan error)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		done <- collector.Wait(ctx)
	}()

	// Add signatures
	time.Sleep(10 * time.Millisecond)
	collector.AddSignature(&ValidatorSignature{ValidatorIndex: 0})
	collector.AddSignature(&ValidatorSignature{ValidatorIndex: 1})

	// Should complete
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Wait returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("Wait timed out")
	}
}

func TestSignatureCollectorTimeout(t *testing.T) {
	message := types.Hash{1, 2, 3}
	collector := NewSignatureCollector(message, 100) // High threshold

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := collector.Wait(ctx)
	if err == nil {
		t.Error("Expected timeout error")
	}
}

func TestValidatorSignatureToSignatureInput(t *testing.T) {
	sig := &ValidatorSignature{
		ValidatorIndex: 5,
		PublicKey:      []byte("test pubkey"),
		Signature:      []byte("test sig"),
		Algorithm:      1,
	}

	input := sig.ToSignatureInput()

	if string(input.PublicKey) != "test pubkey" {
		t.Error("PublicKey mismatch")
	}
	if string(input.Signature) != "test sig" {
		t.Error("Signature mismatch")
	}
	if input.Algorithm != 1 {
		t.Errorf("Algorithm = %d, want 1", input.Algorithm)
	}
}

func TestAggregateBlockSignatures(t *testing.T) {
	blockHash := types.Hash{1, 2, 3, 4}
	signatures := generateTestValidatorSignatures(5)

	aggSig, err := AggregateBlockSignatures(blockHash, signatures)
	if err != nil {
		t.Fatalf("AggregateBlockSignatures failed: %v", err)
	}

	err = VerifyBlockSignatures(aggSig)
	if err != nil {
		t.Errorf("VerifyBlockSignatures failed: %v", err)
	}
}

func TestCreateSignerBitmap(t *testing.T) {
	bitmap := CreateSignerBitmap(100, []uint64{0, 5, 10, 99})

	// Check specific signers
	signers := ParseSignerBitmap(bitmap)
	if len(signers) != 4 {
		t.Errorf("ParseSignerBitmap returned %d signers, want 4", len(signers))
	}

	expectedSigners := map[uint64]bool{0: true, 5: true, 10: true, 99: true}
	for _, s := range signers {
		if !expectedSigners[s] {
			t.Errorf("Unexpected signer %d in bitmap", s)
		}
	}
}

func TestCountSigners(t *testing.T) {
	bitmap := CreateSignerBitmap(100, []uint64{0, 1, 2, 3, 4})
	count := CountSigners(bitmap)
	if count != 5 {
		t.Errorf("CountSigners = %d, want 5", count)
	}
}

func TestGlobalAggregator(t *testing.T) {
	agg1 := GetGlobalAggregator()
	agg2 := GetGlobalAggregator()

	if agg1 != agg2 {
		t.Error("GetGlobalAggregator should return same instance")
	}
}

func TestSTARKAggregatedSignatureBytes(t *testing.T) {
	aggregator := NewSTARKAggregator(DefaultSTARKAggregatorConfig())
	message := []byte("test")
	signatures := generateTestValidatorSignatures(3)

	aggSig, _ := aggregator.Aggregate(message, signatures)

	bytes := aggSig.Bytes()
	if len(bytes) == 0 {
		t.Error("Bytes should not be empty")
	}
	if len(bytes) != len(aggSig.Proof) {
		t.Error("Bytes should equal Proof")
	}
}

// Benchmarks

func BenchmarkSTARKAggregate10(b *testing.B) {
	aggregator := NewSTARKAggregator(DefaultSTARKAggregatorConfig())
	message := []byte("benchmark")
	signatures := generateTestValidatorSignatures(10)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		aggregator.Aggregate(message, signatures)
	}
}

func BenchmarkSTARKAggregate100(b *testing.B) {
	aggregator := NewSTARKAggregator(DefaultSTARKAggregatorConfig())
	message := []byte("benchmark")
	signatures := generateTestValidatorSignatures(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		aggregator.Aggregate(message, signatures)
	}
}

func BenchmarkSTARKVerify(b *testing.B) {
	aggregator := NewSTARKAggregator(DefaultSTARKAggregatorConfig())
	message := []byte("benchmark")
	signatures := generateTestValidatorSignatures(100)
	aggSig, _ := aggregator.Aggregate(message, signatures)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		aggregator.Verify(aggSig)
	}
}
