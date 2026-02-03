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

package transaction

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

func TestEstimatePQTxSizeInfo(t *testing.T) {
	tests := []struct {
		algo       uint8
		pubKeyMode uint8
		dataLen    int
		wantPubKey int
	}{
		{PQAlgoFalcon512, PubKeyModeFull, 0, Falcon512PublicKeySize},
		{PQAlgoFalcon512, PubKeyModeHash, 0, PQPublicKeyHashSize},
		{PQAlgoSQIsign, PubKeyModeFull, 0, SQIsignPublicKeySize},
		{PQAlgoSQIsign, PubKeyModeHash, 0, PQPublicKeyHashSize},
		{PQAlgoDilithium2, PubKeyModeFull, 0, Dilithium2PublicKeySize},
		{PQAlgoDilithium2, PubKeyModeHash, 0, PQPublicKeyHashSize},
		{PQAlgoDilithium3, PubKeyModeFull, 0, Dilithium3PublicKeySize},
		{PQAlgoDilithium3, PubKeyModeHash, 0, PQPublicKeyHashSize},
	}

	for _, tt := range tests {
		info := EstimatePQTxSizeInfo(tt.algo, tt.pubKeyMode, tt.dataLen, nil)
		if info.PubKeySize != tt.wantPubKey {
			t.Errorf("algo=%d, mode=%d: PubKeySize = %d, want %d",
				tt.algo, tt.pubKeyMode, info.PubKeySize, tt.wantPubKey)
		}
		if info.TotalSize <= 0 {
			t.Errorf("algo=%d, mode=%d: TotalSize should be positive",
				tt.algo, tt.pubKeyMode)
		}
	}
}

func TestEstimateFirstVsSubsequentTxSize(t *testing.T) {
	algos := []struct {
		algo uint8
		name string
	}{
		{PQAlgoFalcon512, "Falcon-512"},
		{PQAlgoSQIsign, "SQIsign-I"},
		{PQAlgoDilithium2, "Dilithium2"},
		{PQAlgoDilithium3, "Dilithium3"},
	}

	for _, a := range algos {
		firstSize := EstimateFirstTxSize(a.algo, 0)
		subSize := EstimateSubsequentTxSize(a.algo, 0)

		if subSize >= firstSize {
			t.Errorf("%s: Subsequent tx size (%d) should be smaller than first tx size (%d)",
				a.name, subSize, firstSize)
		}

		savings := GetSizeSavings(a.algo)
		expectedSavings := firstSize - subSize
		if savings != expectedSavings {
			t.Errorf("%s: GetSizeSavings = %d, want %d",
				a.name, savings, expectedSavings)
		}
	}
}

func TestGetAllSizeComparisons(t *testing.T) {
	comparisons := GetAllSizeComparisons()

	if len(comparisons) != 4 {
		t.Errorf("Expected 4 algorithms, got %d", len(comparisons))
	}

	for _, c := range comparisons {
		if c.FirstTxSize <= 0 {
			t.Errorf("%s: FirstTxSize should be positive", c.Algorithm)
		}
		if c.SubsequentSize <= 0 {
			t.Errorf("%s: SubsequentSize should be positive", c.Algorithm)
		}
		if c.Savings <= 0 {
			t.Errorf("%s: Savings should be positive", c.Algorithm)
		}
		if c.SavingsPercent <= 0 || c.SavingsPercent >= 100 {
			t.Errorf("%s: SavingsPercent = %.2f, should be between 0 and 100",
				c.Algorithm, c.SavingsPercent)
		}

		t.Logf("%s: First=%dB, Subsequent=%dB, Savings=%dB (%.1f%%)",
			c.Algorithm, c.FirstTxSize, c.SubsequentSize, c.Savings, c.SavingsPercent)
	}
}

// mockRegistry implements PQPublicKeyRegistry for testing
type mockRegistry struct {
	keys map[types.Hash][]byte
}

func newMockRegistry() *mockRegistry {
	return &mockRegistry{
		keys: make(map[types.Hash][]byte),
	}
}

func (r *mockRegistry) GetPublicKey(keyHash types.Hash) ([]byte, error) {
	if pk, ok := r.keys[keyHash]; ok {
		return pk, nil
	}
	return nil, nil
}

func (r *mockRegistry) RegisterPublicKey(pubKey []byte, algo uint8) (types.Hash, error) {
	keyHash := hash.Hash(pubKey)
	r.keys[keyHash] = pubKey
	return keyHash, nil
}

func (r *mockRegistry) HasPublicKey(keyHash types.Hash) bool {
	_, ok := r.keys[keyHash]
	return ok
}

func (r *mockRegistry) RegisterKey(pubKey []byte) types.Hash {
	keyHash := hash.Hash(pubKey)
	r.keys[keyHash] = pubKey
	return keyHash
}

func TestOptimizedPQTxBuilder(t *testing.T) {
	registry := newMockRegistry()
	chainID := uint256.NewInt(1)
	pubKey := make([]byte, Falcon512PublicKeySize)
	for i := range pubKey {
		pubKey[i] = byte(i % 256)
	}

	builder := NewOptimizedPQTxBuilder(registry, chainID, PQAlgoFalcon512, pubKey)

	// Key not registered yet
	if builder.IsKeyRegistered() {
		t.Error("Key should not be registered initially")
	}
	if builder.ShouldUseHashMode() {
		t.Error("Should not use hash mode when key not registered")
	}

	// Register the key
	registry.RegisterKey(pubKey)

	// Now key is registered
	if !builder.IsKeyRegistered() {
		t.Error("Key should be registered after registration")
	}
	if !builder.ShouldUseHashMode() {
		t.Error("Should use hash mode when key is registered")
	}

	// Build a transaction
	tx := builder.BuildTransaction(
		1,                      // nonce
		nil,                    // to
		uint256.NewInt(0),      // value
		21000,                  // gas
		uint256.NewInt(1000),   // gasTipCap
		uint256.NewInt(10000),  // gasFeeCap
		nil,                    // data
		nil,                    // accessList
	)

	// Should use hash mode
	if tx.PubKeyMode != PubKeyModeHash {
		t.Errorf("PubKeyMode = %d, want %d", tx.PubKeyMode, PubKeyModeHash)
	}
	if len(tx.PubKeyData) != PQPublicKeyHashSize {
		t.Errorf("PubKeyData len = %d, want %d", len(tx.PubKeyData), PQPublicKeyHashSize)
	}
}

func TestOptimizedPQTxBuilderNoRegistry(t *testing.T) {
	chainID := uint256.NewInt(1)
	pubKey := make([]byte, Falcon512PublicKeySize)

	builder := NewOptimizedPQTxBuilder(nil, chainID, PQAlgoFalcon512, pubKey)

	if builder.IsKeyRegistered() {
		t.Error("Key should not be registered with nil registry")
	}
	if builder.ShouldUseHashMode() {
		t.Error("Should not use hash mode with nil registry")
	}

	tx := builder.BuildTransaction(
		1, nil, uint256.NewInt(0), 21000,
		uint256.NewInt(1000), uint256.NewInt(10000),
		nil, nil,
	)

	// Should use full key mode
	if tx.PubKeyMode != PubKeyModeFull {
		t.Errorf("PubKeyMode = %d, want %d", tx.PubKeyMode, PubKeyModeFull)
	}
}

func TestValidatePQTxWithRegistry(t *testing.T) {
	registry := newMockRegistry()
	pubKey := make([]byte, Falcon512PublicKeySize)
	for i := range pubKey {
		pubKey[i] = byte(i % 256)
	}
	keyHash := registry.RegisterKey(pubKey)

	// Test full key mode
	txFull := &PostQuantumTx{
		SigAlgo:    PQAlgoFalcon512,
		PubKeyMode: PubKeyModeFull,
		PubKeyData: pubKey,
	}
	recovered, err := ValidatePQTxWithRegistry(txFull, registry)
	if err != nil {
		t.Fatalf("ValidatePQTxWithRegistry failed for full key: %v", err)
	}
	if len(recovered) != Falcon512PublicKeySize {
		t.Errorf("Recovered key size = %d, want %d", len(recovered), Falcon512PublicKeySize)
	}

	// Test hash mode
	txHash := &PostQuantumTx{
		SigAlgo:    PQAlgoFalcon512,
		PubKeyMode: PubKeyModeHash,
		PubKeyData: keyHash[:],
	}
	recovered, err = ValidatePQTxWithRegistry(txHash, registry)
	if err != nil {
		t.Fatalf("ValidatePQTxWithRegistry failed for hash mode: %v", err)
	}
	if len(recovered) != Falcon512PublicKeySize {
		t.Errorf("Recovered key size = %d, want %d", len(recovered), Falcon512PublicKeySize)
	}

	// Test hash mode with unregistered key
	txUnknown := &PostQuantumTx{
		SigAlgo:    PQAlgoFalcon512,
		PubKeyMode: PubKeyModeHash,
		PubKeyData: make([]byte, PQPublicKeyHashSize),
	}
	_, err = ValidatePQTxWithRegistry(txUnknown, registry)
	if err != ErrPQPubKeyNotFound {
		t.Errorf("Expected ErrPQPubKeyNotFound, got %v", err)
	}
}

func TestPrepareKeyRegistration(t *testing.T) {
	pubKey := make([]byte, Falcon512PublicKeySize)
	for i := range pubKey {
		pubKey[i] = byte(i % 256)
	}

	data, err := PrepareKeyRegistration(pubKey, PQAlgoFalcon512)
	if err != nil {
		t.Fatalf("PrepareKeyRegistration failed: %v", err)
	}

	if len(data.PublicKey) != Falcon512PublicKeySize {
		t.Errorf("PublicKey size = %d, want %d", len(data.PublicKey), Falcon512PublicKeySize)
	}
	if data.Algorithm != PQAlgoFalcon512 {
		t.Errorf("Algorithm = %d, want %d", data.Algorithm, PQAlgoFalcon512)
	}
	if data.KeyHash == (types.Hash{}) {
		t.Error("KeyHash should not be zero")
	}

	// Test invalid size
	_, err = PrepareKeyRegistration([]byte{1, 2, 3}, PQAlgoFalcon512)
	if err != ErrInvalidPQPublicKey {
		t.Errorf("Expected ErrInvalidPQPublicKey, got %v", err)
	}
}

func TestBatchTxOptimizer(t *testing.T) {
	registry := newMockRegistry()
	pubKey := make([]byte, Falcon512PublicKeySize)
	for i := range pubKey {
		pubKey[i] = byte(i % 256)
	}

	// Test with unregistered key
	opt := NewBatchTxOptimizer(registry, pubKey, PQAlgoFalcon512)

	// First transaction should use full key
	tx1 := NewPostQuantumTx(
		uint256.NewInt(1), 0, nil, uint256.NewInt(0),
		21000, uint256.NewInt(1000), uint256.NewInt(10000),
		nil, nil, PQAlgoFalcon512,
	)
	opt.OptimizeTransaction(tx1)
	if tx1.PubKeyMode != PubKeyModeFull {
		t.Errorf("First tx PubKeyMode = %d, want %d", tx1.PubKeyMode, PubKeyModeFull)
	}

	// Second transaction should use hash
	tx2 := NewPostQuantumTx(
		uint256.NewInt(1), 1, nil, uint256.NewInt(0),
		21000, uint256.NewInt(1000), uint256.NewInt(10000),
		nil, nil, PQAlgoFalcon512,
	)
	opt.OptimizeTransaction(tx2)
	if tx2.PubKeyMode != PubKeyModeHash {
		t.Errorf("Second tx PubKeyMode = %d, want %d", tx2.PubKeyMode, PubKeyModeHash)
	}

	// Third transaction should also use hash
	tx3 := NewPostQuantumTx(
		uint256.NewInt(1), 2, nil, uint256.NewInt(0),
		21000, uint256.NewInt(1000), uint256.NewInt(10000),
		nil, nil, PQAlgoFalcon512,
	)
	opt.OptimizeTransaction(tx3)
	if tx3.PubKeyMode != PubKeyModeHash {
		t.Errorf("Third tx PubKeyMode = %d, want %d", tx3.PubKeyMode, PubKeyModeHash)
	}
}

func TestBatchTxOptimizerWithRegisteredKey(t *testing.T) {
	registry := newMockRegistry()
	pubKey := make([]byte, Falcon512PublicKeySize)
	for i := range pubKey {
		pubKey[i] = byte(i % 256)
	}

	// Register key first
	registry.RegisterKey(pubKey)

	// Create optimizer - key is already registered
	opt := NewBatchTxOptimizer(registry, pubKey, PQAlgoFalcon512)

	// Even first transaction should use hash since key is already registered
	tx := NewPostQuantumTx(
		uint256.NewInt(1), 0, nil, uint256.NewInt(0),
		21000, uint256.NewInt(1000), uint256.NewInt(10000),
		nil, nil, PQAlgoFalcon512,
	)
	opt.OptimizeTransaction(tx)
	if tx.PubKeyMode != PubKeyModeHash {
		t.Errorf("PubKeyMode = %d, want %d (key already registered)", tx.PubKeyMode, PubKeyModeHash)
	}
}

func TestBatchSizeEstimation(t *testing.T) {
	opt := NewBatchTxOptimizer(nil, make([]byte, Falcon512PublicKeySize), PQAlgoFalcon512)

	// Test batch of 10 transactions
	batchSize := opt.GetEstimatedBatchSize(10, 0)
	withoutOpt := opt.GetEstimatedBatchSizeWithoutOptimization(10, 0)
	savings := opt.GetBatchSavings(10, 0)

	if batchSize >= withoutOpt {
		t.Errorf("Optimized batch (%d) should be smaller than unoptimized (%d)",
			batchSize, withoutOpt)
	}
	if savings != withoutOpt-batchSize {
		t.Errorf("Savings = %d, want %d", savings, withoutOpt-batchSize)
	}

	// Expected savings = (pubKeySize - hashSize) * 9 transactions
	expectedSavings := (Falcon512PublicKeySize - PQPublicKeyHashSize) * 9
	if savings != expectedSavings {
		t.Errorf("Savings = %d, want %d", savings, expectedSavings)
	}

	t.Logf("Batch of 10 Falcon-512 txs: Optimized=%dB, Unoptimized=%dB, Savings=%dB (%.1f%%)",
		batchSize, withoutOpt, savings, float64(savings)/float64(withoutOpt)*100)
}

func TestGetExpectedPubKeySize(t *testing.T) {
	tests := []struct {
		algo uint8
		want int
	}{
		{PQAlgoFalcon512, Falcon512PublicKeySize},
		{PQAlgoSQIsign, SQIsignPublicKeySize},
		{PQAlgoDilithium2, Dilithium2PublicKeySize},
		{PQAlgoDilithium3, Dilithium3PublicKeySize},
		{255, 0}, // Unknown
	}

	for _, tt := range tests {
		got := GetExpectedPubKeySize(tt.algo)
		if got != tt.want {
			t.Errorf("GetExpectedPubKeySize(%d) = %d, want %d", tt.algo, got, tt.want)
		}
	}
}

func TestGetExpectedSignatureSize(t *testing.T) {
	tests := []struct {
		algo uint8
		want int
	}{
		{PQAlgoFalcon512, Falcon512SignatureSize},
		{PQAlgoSQIsign, SQIsignSignatureSize},
		{PQAlgoDilithium2, Dilithium2SignatureSize},
		{PQAlgoDilithium3, Dilithium3SignatureSize},
		{255, 0}, // Unknown
	}

	for _, tt := range tests {
		got := GetExpectedSignatureSize(tt.algo)
		if got != tt.want {
			t.Errorf("GetExpectedSignatureSize(%d) = %d, want %d", tt.algo, got, tt.want)
		}
	}
}
