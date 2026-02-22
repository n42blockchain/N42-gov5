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

// Package pqregistry provides Go bindings and utilities for the
// Post-Quantum Public Key Registry contract.
package pqregistry

import (
	"errors"
	"sync"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// Algorithm identifiers matching the Solidity contract
const (
	AlgoFalcon512  uint8 = 0
	AlgoSQIsign    uint8 = 1
	AlgoDilithium2 uint8 = 2
	AlgoDilithium3 uint8 = 3
)

// Expected public key sizes for each algorithm
const (
	Falcon512PubKeySize  = 897
	SQIsignPubKeySize    = 64
	Dilithium2PubKeySize = 1312
	Dilithium3PubKeySize = 1952
)

var (
	ErrInvalidAlgorithm  = errors.New("pqregistry: invalid algorithm")
	ErrInvalidPubKeySize = errors.New("pqregistry: invalid public key size")
	ErrKeyAlreadyExists  = errors.New("pqregistry: key already registered")
	ErrKeyNotFound       = errors.New("pqregistry: key not found")
	ErrKeyRevoked        = errors.New("pqregistry: key has been revoked")
	ErrNotKeyOwner       = errors.New("pqregistry: caller is not key owner")
)

// KeyData represents the data stored for a registered key
type KeyData struct {
	PubKey       []byte
	Owner        types.Address
	Algorithm    uint8
	RegisteredAt uint64
	Revoked      bool
}

// Registry is an in-memory implementation of the PQ Key Registry
// for use in transaction validation and testing.
// In production, this would interact with the on-chain contract.
type Registry struct {
	mu               sync.RWMutex
	keys             map[types.Hash]*KeyData
	addressToKeyHash map[types.Address]types.Hash
}

// NewRegistry creates a new in-memory PQ key registry
func NewRegistry() *Registry {
	return &Registry{
		keys:             make(map[types.Hash]*KeyData),
		addressToKeyHash: make(map[types.Address]types.Hash),
	}
}

// RegisterKey registers a public key and returns its hash.
func (r *Registry) RegisterKey(pubKey []byte, algorithm uint8, owner types.Address) (types.Hash, error) {
	if algorithm > AlgoDilithium3 {
		return types.Hash{}, ErrInvalidAlgorithm
	}

	expectedSize := GetExpectedKeySize(algorithm)
	if len(pubKey) != expectedSize {
		return types.Hash{}, ErrInvalidPubKeySize
	}

	keyHash := hash.Hash(pubKey)

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.keys[keyHash]; exists {
		return types.Hash{}, ErrKeyAlreadyExists
	}

	r.keys[keyHash] = &KeyData{
		PubKey:       append([]byte{}, pubKey...), // deep copy
		Owner:        owner,
		Algorithm:    algorithm,
		RegisteredAt: 0, // would be block.timestamp in on-chain contract
		Revoked:      false,
	}
	r.addressToKeyHash[owner] = keyHash

	return keyHash, nil
}

// RevokeKey revokes a registered key. Only the key owner can revoke it.
func (r *Registry) RevokeKey(keyHash types.Hash, caller types.Address) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	keyData, exists := r.keys[keyHash]
	if !exists {
		return ErrKeyNotFound
	}
	if keyData.Owner != caller {
		return ErrNotKeyOwner
	}
	if keyData.Revoked {
		return ErrKeyRevoked
	}

	keyData.Revoked = true
	if r.addressToKeyHash[caller] == keyHash {
		delete(r.addressToKeyHash, caller)
	}

	return nil
}

// GetPublicKey retrieves a public key by its hash. Returns an error if revoked.
func (r *Registry) GetPublicKey(keyHash types.Hash) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyData, exists := r.keys[keyHash]
	if !exists {
		return nil, ErrKeyNotFound
	}
	if keyData.Revoked {
		return nil, ErrKeyRevoked
	}

	return append([]byte{}, keyData.PubKey...), nil
}

// GetKeyHashByAddress returns the key hash associated with an address.
func (r *Registry) GetKeyHashByAddress(account types.Address) (types.Hash, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyHash, exists := r.addressToKeyHash[account]
	return keyHash, exists
}

// GetKeyByAddress returns the full public key for an address.
func (r *Registry) GetKeyByAddress(account types.Address) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyHash, exists := r.addressToKeyHash[account]
	if !exists {
		return nil, ErrKeyNotFound
	}

	keyData := r.keys[keyHash]
	if keyData.Revoked {
		return nil, ErrKeyRevoked
	}

	return append([]byte{}, keyData.PubKey...), nil
}

// HasKey reports whether a key hash exists and is not revoked.
func (r *Registry) HasKey(keyHash types.Hash) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyData, exists := r.keys[keyHash]
	return exists && !keyData.Revoked
}

// HasKeyForAddress reports whether an address has an active (non-revoked) registered key.
func (r *Registry) HasKeyForAddress(account types.Address) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyHash, exists := r.addressToKeyHash[account]
	if !exists {
		return false
	}

	keyData := r.keys[keyHash]
	return keyData != nil && !keyData.Revoked
}

// GetAlgorithm returns the algorithm identifier for a registered key.
func (r *Registry) GetAlgorithm(keyHash types.Hash) (uint8, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyData, exists := r.keys[keyHash]
	if !exists {
		return 0, ErrKeyNotFound
	}

	return keyData.Algorithm, nil
}

// GetKeyOwner returns the owner address of a registered key.
func (r *Registry) GetKeyOwner(keyHash types.Hash) (types.Address, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyData, exists := r.keys[keyHash]
	if !exists {
		return types.Address{}, ErrKeyNotFound
	}

	return keyData.Owner, nil
}

// GetKeyData returns a deep copy of all data for a registered key.
func (r *Registry) GetKeyData(keyHash types.Hash) (*KeyData, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyData, exists := r.keys[keyHash]
	if !exists {
		return nil, ErrKeyNotFound
	}

	return &KeyData{
		PubKey:       append([]byte{}, keyData.PubKey...),
		Owner:        keyData.Owner,
		Algorithm:    keyData.Algorithm,
		RegisteredAt: keyData.RegisteredAt,
		Revoked:      keyData.Revoked,
	}, nil
}

// GetExpectedKeySize returns the expected key size for an algorithm
func GetExpectedKeySize(algorithm uint8) int {
	switch algorithm {
	case AlgoFalcon512:
		return Falcon512PubKeySize
	case AlgoSQIsign:
		return SQIsignPubKeySize
	case AlgoDilithium2:
		return Dilithium2PubKeySize
	case AlgoDilithium3:
		return Dilithium3PubKeySize
	default:
		return 0
	}
}

// GetAlgorithmName returns the name of an algorithm
func GetAlgorithmName(algorithm uint8) string {
	switch algorithm {
	case AlgoFalcon512:
		return "Falcon-512"
	case AlgoSQIsign:
		return "SQIsign-I"
	case AlgoDilithium2:
		return "Dilithium2"
	case AlgoDilithium3:
		return "Dilithium3"
	default:
		return "Unknown"
	}
}

// ComputeKeyHash computes the hash of a public key
func ComputeKeyHash(pubKey []byte) types.Hash {
	return hash.Hash(pubKey)
}

// ValidatePublicKey validates a public key for a given algorithm
func ValidatePublicKey(pubKey []byte, algorithm uint8) error {
	if algorithm > AlgoDilithium3 {
		return ErrInvalidAlgorithm
	}

	expectedSize := GetExpectedKeySize(algorithm)
	if len(pubKey) != expectedSize {
		return ErrInvalidPubKeySize
	}

	return nil
}

var (
	globalRegistry     *Registry
	globalRegistryOnce sync.Once
)

// GetGlobalRegistry returns the global registry instance
func GetGlobalRegistry() *Registry {
	globalRegistryOnce.Do(func() {
		globalRegistry = NewRegistry()
	})
	return globalRegistry
}

// SetGlobalRegistry sets the global registry instance (for testing)
func SetGlobalRegistry(r *Registry) {
	globalRegistry = r
}
