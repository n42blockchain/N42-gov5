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

package apos

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
)

func generateTestValidators(count int) []*api.ValidatorSignature {
	validators := make([]*api.ValidatorSignature, count)
	for i := 0; i < count; i++ {
		pubKey := make([]byte, 897) // Falcon-512 size
		sig := make([]byte, 666)
		rand.Read(pubKey)
		rand.Read(sig)
		validators[i] = &api.ValidatorSignature{
			ValidatorIndex: uint64(i),
			PublicKey:      pubKey,
			Signature:      sig,
			Algorithm:      0, // Falcon
		}
	}
	return validators
}

func TestPostQuantumConfig(t *testing.T) {
	config := DefaultPostQuantumConfig()

	if config.Mode != PQModeDisabled {
		t.Errorf("Default mode should be disabled, got %d", config.Mode)
	}
	if config.IsEnabled() {
		t.Error("Default config should not be enabled")
	}

	config.Mode = PQModeHybrid
	if !config.IsEnabled() {
		t.Error("Hybrid mode should be enabled")
	}

	config.ForkHeight = 1000
	if config.ShouldUsePQ(500) {
		t.Error("Should not use PQ before fork height")
	}
	if !config.ShouldUsePQ(1000) {
		t.Error("Should use PQ at fork height")
	}
	if !config.ShouldUsePQ(2000) {
		t.Error("Should use PQ after fork height")
	}
}

func TestSTARKConsensusManager(t *testing.T) {
	config := &PostQuantumConfig{
		Mode:          PQModeHybrid,
		ForkHeight:    0,
		MinValidators: 3,
		FallbackToBLS: true,
	}
	manager := NewSTARKConsensusManager(config)

	if manager.GetPQMode() != PQModeHybrid {
		t.Errorf("Mode = %d, want %d", manager.GetPQMode(), PQModeHybrid)
	}
	if manager.GetMinValidators() != 3 {
		t.Errorf("MinValidators = %d, want 3", manager.GetMinValidators())
	}
}

func TestSTARKConsensusManagerModeSwitch(t *testing.T) {
	manager := NewSTARKConsensusManager(nil)

	// Default should be disabled
	if manager.GetPQMode() != PQModeDisabled {
		t.Error("Default mode should be disabled")
	}

	// Switch to hybrid
	manager.SetPQMode(PQModeHybrid)
	if manager.GetPQMode() != PQModeHybrid {
		t.Error("Mode should be hybrid after switch")
	}

	// Switch to PQ only
	manager.SetPQMode(PQModeOnly)
	if manager.GetPQMode() != PQModeOnly {
		t.Error("Mode should be PQ-only after switch")
	}
}

func TestCollectSTARKSignatures(t *testing.T) {
	config := &PostQuantumConfig{
		Mode:          PQModeHybrid,
		MinValidators: 3,
	}
	manager := NewSTARKConsensusManager(config)
	validators := generateTestValidators(5)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	blockHash := types.Hash{1, 2, 3, 4}

	sig, err := manager.CollectSTARKSignatures(ctx, blockHash, validators)
	if err != nil {
		t.Fatalf("CollectSTARKSignatures failed: %v", err)
	}

	if sig.SignerCount != 5 {
		t.Errorf("SignerCount = %d, want 5", sig.SignerCount)
	}
	if len(sig.Proof) == 0 {
		t.Error("Proof should not be empty")
	}
	if len(sig.SignerCommitments) != 5 {
		t.Errorf("SignerCommitments count = %d, want 5", len(sig.SignerCommitments))
	}

	t.Logf("STARK signature: %d signers, %d bytes proof", sig.SignerCount, len(sig.Proof))
}

func TestCollectSTARKSignaturesInsufficientValidators(t *testing.T) {
	config := &PostQuantumConfig{
		Mode:          PQModeHybrid,
		MinValidators: 10,
	}
	manager := NewSTARKConsensusManager(config)
	validators := generateTestValidators(5) // Less than MinValidators

	ctx := context.Background()
	blockHash := types.Hash{1, 2, 3}

	_, err := manager.CollectSTARKSignatures(ctx, blockHash, validators)
	if err != errInsufficientSigners {
		t.Errorf("Expected errInsufficientSigners, got %v", err)
	}
}

func TestVerifySTARKSignature(t *testing.T) {
	config := &PostQuantumConfig{
		Mode:          PQModeHybrid,
		MinValidators: 3,
	}
	manager := NewSTARKConsensusManager(config)
	validators := generateTestValidators(5)

	ctx := context.Background()
	blockHash := types.Hash{1, 2, 3, 4}

	// Collect signatures
	sig, err := manager.CollectSTARKSignatures(ctx, blockHash, validators)
	if err != nil {
		t.Fatalf("CollectSTARKSignatures failed: %v", err)
	}

	// Verify
	err = manager.VerifySTARKSignature(sig)
	if err != nil {
		t.Errorf("VerifySTARKSignature failed: %v", err)
	}
}

func TestVerifySTARKSignatureMissing(t *testing.T) {
	manager := NewSTARKConsensusManager(nil)

	err := manager.VerifySTARKSignature(nil)
	if err != errMissingSTARKProof {
		t.Errorf("Expected errMissingSTARKProof, got %v", err)
	}

	err = manager.VerifySTARKSignature(&STARKBlockSignature{})
	if err != errMissingSTARKProof {
		t.Errorf("Expected errMissingSTARKProof for empty proof, got %v", err)
	}
}

func TestHybridBlockSignature(t *testing.T) {
	hybrid := &HybridBlockSignature{}

	if hybrid.HasBLS() {
		t.Error("Empty signature should not have BLS")
	}
	if hybrid.HasSTARK() {
		t.Error("Empty signature should not have STARK")
	}

	// Set BLS
	hybrid.BLSSignature = [96]byte{1, 2, 3}
	if !hybrid.HasBLS() {
		t.Error("Should have BLS after setting")
	}

	// Set STARK
	hybrid.STARKSignature = &STARKBlockSignature{
		Proof: []byte{1, 2, 3},
	}
	if !hybrid.HasSTARK() {
		t.Error("Should have STARK after setting")
	}
}

func TestVerifySealPQDisabled(t *testing.T) {
	config := &PostQuantumConfig{
		Mode: PQModeDisabled,
	}
	manager := NewSTARKConsensusManager(config)

	// Should pass with BLS only
	err := manager.VerifySealPQ([]byte{1, 2, 3}, nil, 0)
	if err != nil {
		t.Errorf("VerifySealPQ should pass in disabled mode: %v", err)
	}

	// Should fail without BLS
	err = manager.VerifySealPQ(nil, nil, 0)
	if err == nil {
		t.Error("VerifySealPQ should fail without BLS in disabled mode")
	}
}

func TestVerifySealPQOnly(t *testing.T) {
	config := &PostQuantumConfig{
		Mode:          PQModeOnly,
		MinValidators: 3,
	}
	manager := NewSTARKConsensusManager(config)
	validators := generateTestValidators(5)

	ctx := context.Background()
	blockHash := types.Hash{1, 2, 3}

	// Get STARK signature
	starkSig, _ := manager.CollectSTARKSignatures(ctx, blockHash, validators)

	// Should pass with STARK
	err := manager.VerifySealPQ(nil, starkSig, 0)
	if err != nil {
		t.Errorf("VerifySealPQ should pass with STARK: %v", err)
	}

	// Should fail without STARK
	err = manager.VerifySealPQ([]byte{1, 2, 3}, nil, 0)
	if err != errMissingSTARKProof {
		t.Errorf("Expected errMissingSTARKProof, got %v", err)
	}
}

func TestGlobalSTARKManager(t *testing.T) {
	manager1 := GetGlobalSTARKManager()
	manager2 := GetGlobalSTARKManager()

	if manager1 != manager2 {
		t.Error("GetGlobalSTARKManager should return same instance")
	}
}

func TestGetModeString(t *testing.T) {
	tests := []struct {
		mode PostQuantumMode
		want string
	}{
		{PQModeDisabled, "disabled"},
		{PQModeHybrid, "hybrid"},
		{PQModeOnly, "pq-only"},
		{PostQuantumMode(99), "unknown"},
	}

	for _, tt := range tests {
		got := GetModeString(tt.mode)
		if got != tt.want {
			t.Errorf("GetModeString(%d) = %s, want %s", tt.mode, got, tt.want)
		}
	}
}

func TestEstimateSTARKProofSize(t *testing.T) {
	sizes := []struct {
		validators int
		minSize    int
	}{
		{10, 100},
		{100, 100},
		{500, 100},
	}

	for _, s := range sizes {
		size := EstimateSTARKProofSize(s.validators)
		if size < s.minSize {
			t.Errorf("EstimateSTARKProofSize(%d) = %d, should be at least %d",
				s.validators, size, s.minSize)
		}
		t.Logf("Estimated proof size for %d validators: %d bytes", s.validators, size)
	}
}

func TestSTARKBlockSignature(t *testing.T) {
	sig := &STARKBlockSignature{
		Proof:       []byte{1, 2, 3, 4, 5},
		SignerCount: 10,
	}

	if sig.Size() != 5 {
		t.Errorf("Size = %d, want 5", sig.Size())
	}

	bytes := sig.Bytes()
	if len(bytes) != 5 {
		t.Errorf("Bytes len = %d, want 5", len(bytes))
	}
}

func TestIsPQActive(t *testing.T) {
	config := &PostQuantumConfig{
		Mode:       PQModeHybrid,
		ForkHeight: 1000,
	}
	manager := NewSTARKConsensusManager(config)

	if manager.IsPQActive(500) {
		t.Error("Should not be active before fork height")
	}
	if !manager.IsPQActive(1000) {
		t.Error("Should be active at fork height")
	}
	if !manager.IsPQActive(2000) {
		t.Error("Should be active after fork height")
	}
}

// Benchmarks

func BenchmarkCollectSTARKSignatures(b *testing.B) {
	config := &PostQuantumConfig{
		Mode:          PQModeHybrid,
		MinValidators: 1,
	}
	manager := NewSTARKConsensusManager(config)
	validators := generateTestValidators(100)
	ctx := context.Background()
	blockHash := types.Hash{1, 2, 3}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.CollectSTARKSignatures(ctx, blockHash, validators)
	}
}

func BenchmarkVerifySTARKSignature(b *testing.B) {
	config := &PostQuantumConfig{
		Mode:          PQModeHybrid,
		MinValidators: 1,
	}
	manager := NewSTARKConsensusManager(config)
	validators := generateTestValidators(100)
	ctx := context.Background()
	blockHash := types.Hash{1, 2, 3}

	sig, _ := manager.CollectSTARKSignatures(ctx, blockHash, validators)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.VerifySTARKSignature(sig)
	}
}
