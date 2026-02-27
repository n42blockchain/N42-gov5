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

// Post-Quantum Transaction Optimization
//
// This module provides utilities for optimizing PQ transaction sizes:
// - First transaction: Full public key included (for registration)
// - Subsequent transactions: Only 32-byte hash (references registry)
//
// Size savings by algorithm:
// | Algorithm   | First Tx  | Subsequent | Savings |
// |-------------|-----------|------------|---------|
// | Falcon-512  | ~1563B    | ~698B      | ~865B   |
// | SQIsign-I   | ~241B     | ~209B      | ~32B    |
// | Dilithium2  | ~3732B    | ~2452B     | ~1280B  |
// | Dilithium3  | ~5245B    | ~3325B     | ~1920B  |

package transaction

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

const (
	// PubKeyModeFull indicates the transaction contains a full public key
	// Used for first-time transactions or when registry is unavailable
	PubKeyModeFull uint8 = 0

	// PubKeyModeHash indicates the transaction contains a public key hash
	// References the public key stored in the on-chain registry
	PubKeyModeHash uint8 = 1
)

// TxSizeInfo contains size information for a PQ transaction
type TxSizeInfo struct {
	BaseSize      int // Fixed overhead (nonce, gas, value, etc.)
	PubKeySize    int // Public key or hash size
	SignatureSize int // Signature size
	DataSize      int // Transaction data size
	AccessListSize int // Access list size
	TotalSize     int // Total estimated size
}

// EstimatePQTxSizeInfo calculates detailed size information for a PQ transaction
func EstimatePQTxSizeInfo(algo uint8, pubKeyMode uint8, dataLen int, accessList AccessList) TxSizeInfo {
	info := TxSizeInfo{
		BaseSize: 200, // ChainID, Nonce, GasTipCap, GasFeeCap, Gas, To, Value
		DataSize: dataLen,
	}

	// Determine public key size
	if pubKeyMode == PubKeyModeHash {
		info.PubKeySize = PQPublicKeyHashSize
	} else {
		info.PubKeySize = GetExpectedPubKeySize(algo)
	}

	// Determine signature size
	info.SignatureSize = GetExpectedSignatureSize(algo)

	// Calculate access list size
	for _, tuple := range accessList {
		info.AccessListSize += 20 + len(tuple.StorageKeys)*32
	}

	info.TotalSize = info.BaseSize + info.PubKeySize + info.SignatureSize + info.DataSize + info.AccessListSize

	return info
}

// EstimateFirstTxSize estimates the size of a first-time PQ transaction (full public key)
func EstimateFirstTxSize(algo uint8, dataLen int) int {
	info := EstimatePQTxSizeInfo(algo, PubKeyModeFull, dataLen, nil)
	return info.TotalSize
}

// EstimateSubsequentTxSize estimates the size of a subsequent PQ transaction (hash reference)
func EstimateSubsequentTxSize(algo uint8, dataLen int) int {
	info := EstimatePQTxSizeInfo(algo, PubKeyModeHash, dataLen, nil)
	return info.TotalSize
}

// GetSizeSavings returns the size savings from using hash reference
func GetSizeSavings(algo uint8) int {
	return GetExpectedPubKeySize(algo) - PQPublicKeyHashSize
}

// GetExpectedPubKeySize returns the expected public key size for an algorithm
func GetExpectedPubKeySize(algo uint8) int {
	switch algo {
	case PQAlgoFalcon512:
		return Falcon512PublicKeySize
	case PQAlgoSQIsign:
		return SQIsignPublicKeySize
	case PQAlgoDilithium2:
		return Dilithium2PublicKeySize
	case PQAlgoDilithium3:
		return Dilithium3PublicKeySize
	default:
		return 0
	}
}

// GetExpectedSignatureSize returns the expected signature size for an algorithm
func GetExpectedSignatureSize(algo uint8) int {
	switch algo {
	case PQAlgoFalcon512:
		return Falcon512SignatureSize
	case PQAlgoSQIsign:
		return SQIsignSignatureSize
	case PQAlgoDilithium2:
		return Dilithium2SignatureSize
	case PQAlgoDilithium3:
		return Dilithium3SignatureSize
	default:
		return 0
	}
}

// OptimizedPQTxBuilder helps create optimized PQ transactions
type OptimizedPQTxBuilder struct {
	registry   PQPublicKeyRegistry
	chainID    *uint256.Int
	sigAlgo    uint8
	publicKey  []byte
	privateKey interface{} // Algorithm-specific private key
}

// NewOptimizedPQTxBuilder creates a new builder with registry support
func NewOptimizedPQTxBuilder(registry PQPublicKeyRegistry, chainID *uint256.Int, sigAlgo uint8, pubKey []byte) *OptimizedPQTxBuilder {
	return &OptimizedPQTxBuilder{
		registry:  registry,
		chainID:   chainID,
		sigAlgo:   sigAlgo,
		publicKey: pubKey,
	}
}

// IsKeyRegistered checks if the public key is registered in the registry
func (b *OptimizedPQTxBuilder) IsKeyRegistered() bool {
	if b.registry == nil {
		return false
	}
	keyHash := hash.Hash(b.publicKey)
	return b.registry.HasPublicKey(keyHash)
}

// GetKeyHash returns the hash of the public key
func (b *OptimizedPQTxBuilder) GetKeyHash() types.Hash {
	return hash.Hash(b.publicKey)
}

// ShouldUseHashMode determines if hash mode should be used
// Returns true if the key is registered and hash mode provides savings
func (b *OptimizedPQTxBuilder) ShouldUseHashMode() bool {
	// Check if key is registered
	if !b.IsKeyRegistered() {
		return false
	}

	// Hash mode always provides savings if key is registered
	// (except for SQIsign where savings are minimal)
	return true
}

// BuildTransaction creates an optimized PQ transaction
func (b *OptimizedPQTxBuilder) BuildTransaction(
	nonce uint64,
	to *types.Address,
	value *uint256.Int,
	gas uint64,
	gasTipCap *uint256.Int,
	gasFeeCap *uint256.Int,
	data []byte,
	accessList AccessList,
) *PostQuantumTx {
	tx := NewPostQuantumTx(
		b.chainID,
		nonce,
		to,
		value,
		gas,
		gasTipCap,
		gasFeeCap,
		data,
		accessList,
		b.sigAlgo,
	)

	// Use hash mode if key is registered
	useHash := b.ShouldUseHashMode()
	tx.SetPubKey(b.publicKey, useHash)

	return tx
}

// ValidatePQTxWithRegistry validates a PQ transaction using the registry for key lookup
func ValidatePQTxWithRegistry(tx *PostQuantumTx, registry PQPublicKeyRegistry) ([]byte, error) {
	// If full public key is provided, validate size and return it
	if tx.PubKeyMode == PubKeyModeFull {
		expectedSize := tx.GetExpectedPubKeySize()
		if len(tx.PubKeyData) != expectedSize {
			return nil, ErrInvalidPQPublicKey
		}
		return tx.PubKeyData, nil
	}

	// Hash mode - look up in registry
	if registry == nil {
		return nil, ErrPQPubKeyNotFound
	}

	if len(tx.PubKeyData) != PQPublicKeyHashSize {
		return nil, ErrInvalidPQPublicKey
	}

	var keyHash types.Hash
	copy(keyHash[:], tx.PubKeyData)

	pubKey, err := registry.GetPublicKey(keyHash)
	if err != nil {
		return nil, ErrPQPubKeyNotFound
	}
	if pubKey == nil {
		return nil, ErrPQPubKeyNotFound
	}

	return pubKey, nil
}

type TxSizeComparison struct {
	Algorithm       string
	FirstTxSize     int
	SubsequentSize  int
	Savings         int
	SavingsPercent  float64
}

// GetAllSizeComparisons returns size comparisons for all supported algorithms
func GetAllSizeComparisons() []TxSizeComparison {
	algorithms := []struct {
		algo uint8
		name string
	}{
		{PQAlgoFalcon512, "Falcon-512"},
		{PQAlgoSQIsign, "SQIsign-I"},
		{PQAlgoDilithium2, "Dilithium2"},
		{PQAlgoDilithium3, "Dilithium3"},
	}

	comparisons := make([]TxSizeComparison, 0, len(algorithms))
	for _, a := range algorithms {
		firstSize := EstimateFirstTxSize(a.algo, 0)
		subSize := EstimateSubsequentTxSize(a.algo, 0)
		savings := firstSize - subSize

		comparisons = append(comparisons, TxSizeComparison{
			Algorithm:      a.name,
			FirstTxSize:    firstSize,
			SubsequentSize: subSize,
			Savings:        savings,
			SavingsPercent: float64(savings) / float64(firstSize) * 100,
		})
	}

	return comparisons
}

// RegisterKeyData contains data needed for key registration
type RegisterKeyData struct {
	PublicKey []byte
	Algorithm uint8
	KeyHash   types.Hash
}

// PrepareKeyRegistration prepares data for registering a public key
func PrepareKeyRegistration(pubKey []byte, algo uint8) (*RegisterKeyData, error) {
	expectedSize := GetExpectedPubKeySize(algo)
	if len(pubKey) != expectedSize {
		return nil, ErrInvalidPQPublicKey
	}

	return &RegisterKeyData{
		PublicKey: pubKey,
		Algorithm: algo,
		KeyHash:   hash.Hash(pubKey),
	}, nil
}

// BatchTxOptimizer optimizes a batch of transactions for the same sender
type BatchTxOptimizer struct {
	registry    PQPublicKeyRegistry
	publicKey   []byte
	algorithm   uint8
	keyHash     types.Hash
	isFirstTx   bool
}

// NewBatchTxOptimizer creates a new batch optimizer
func NewBatchTxOptimizer(registry PQPublicKeyRegistry, pubKey []byte, algo uint8) *BatchTxOptimizer {
	keyHash := hash.Hash(pubKey)

	// Check if key is already registered
	var isFirst bool
	if registry != nil {
		isFirst = !registry.HasPublicKey(keyHash)
	} else {
		isFirst = true
	}

	return &BatchTxOptimizer{
		registry:  registry,
		publicKey: pubKey,
		algorithm: algo,
		keyHash:   keyHash,
		isFirstTx: isFirst,
	}
}

// OptimizeTransaction optimizes a transaction's public key mode
// The first transaction uses full key, subsequent use hash
func (o *BatchTxOptimizer) OptimizeTransaction(tx *PostQuantumTx) {
	if o.isFirstTx {
		// First transaction needs full key for registration
		tx.PubKeyMode = PubKeyModeFull
		tx.PubKeyData = make([]byte, len(o.publicKey))
		copy(tx.PubKeyData, o.publicKey)
		o.isFirstTx = false // Next transaction will use hash
	} else {
		// Subsequent transactions use hash reference
		tx.PubKeyMode = PubKeyModeHash
		tx.PubKeyData = o.keyHash[:]
	}
}

// GetEstimatedBatchSize estimates the total size for a batch of transactions
func (o *BatchTxOptimizer) GetEstimatedBatchSize(count int, avgDataSize int) int {
	if count == 0 {
		return 0
	}

	// First transaction with full key
	totalSize := EstimateFirstTxSize(o.algorithm, avgDataSize)

	// Remaining transactions with hash
	if count > 1 {
		totalSize += EstimateSubsequentTxSize(o.algorithm, avgDataSize) * (count - 1)
	}

	return totalSize
}

// GetEstimatedBatchSizeWithoutOptimization estimates size without optimization
func (o *BatchTxOptimizer) GetEstimatedBatchSizeWithoutOptimization(count int, avgDataSize int) int {
	return EstimateFirstTxSize(o.algorithm, avgDataSize) * count
}

// GetBatchSavings calculates the total savings from batch optimization
func (o *BatchTxOptimizer) GetBatchSavings(count int, avgDataSize int) int {
	withoutOpt := o.GetEstimatedBatchSizeWithoutOptimization(count, avgDataSize)
	withOpt := o.GetEstimatedBatchSize(count, avgDataSize)
	return withoutOpt - withOpt
}
