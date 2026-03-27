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

package pqregistry

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/n42blockchain/N42/crypto/falcon"
	"github.com/n42blockchain/N42/common/types"
)

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if r.keys == nil || r.addressToKeyHash == nil {
		t.Error("Registry maps not initialized")
	}
}

func TestRegisterKeyFalcon(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Generate Falcon key
	pk, _, err := falcon.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate Falcon key: %v", err)
	}

	pubKey := pk.Bytes()
	keyHash, err := r.RegisterKey(pubKey, AlgoFalcon512, owner)
	if err != nil {
		t.Fatalf("RegisterKey failed: %v", err)
	}

	// Verify key hash is non-zero
	if keyHash == (types.Hash{}) {
		t.Error("Key hash should not be zero")
	}

	// Verify key can be retrieved
	retrieved, err := r.GetPublicKey(keyHash)
	if err != nil {
		t.Fatalf("GetPublicKey failed: %v", err)
	}

	if !bytes.Equal(retrieved, pubKey) {
		t.Error("Retrieved public key doesn't match original")
	}
}

func TestRegisterKeyInvalidAlgorithm(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")
	pubKey := make([]byte, 100)

	_, err := r.RegisterKey(pubKey, 99, owner)
	if err != ErrInvalidAlgorithm {
		t.Errorf("Expected ErrInvalidAlgorithm, got %v", err)
	}
}

func TestRegisterKeyInvalidSize(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")
	pubKey := make([]byte, 100) // Wrong size for Falcon

	_, err := r.RegisterKey(pubKey, AlgoFalcon512, owner)
	if err != ErrInvalidPubKeySize {
		t.Errorf("Expected ErrInvalidPubKeySize, got %v", err)
	}
}

func TestRegisterKeyDuplicate(t *testing.T) {
	r := NewRegistry()
	owner1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	owner2 := types.HexToAddress("0x2222222222222222222222222222222222222222")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	pubKey := pk.Bytes()

	// First registration should succeed
	_, err := r.RegisterKey(pubKey, AlgoFalcon512, owner1)
	if err != nil {
		t.Fatalf("First RegisterKey failed: %v", err)
	}

	// Second registration with same key should fail
	_, err = r.RegisterKey(pubKey, AlgoFalcon512, owner2)
	if err != ErrKeyAlreadyExists {
		t.Errorf("Expected ErrKeyAlreadyExists, got %v", err)
	}
}

func TestRevokeKey(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	pubKey := pk.Bytes()

	keyHash, _ := r.RegisterKey(pubKey, AlgoFalcon512, owner)

	// Revoke the key
	err := r.RevokeKey(keyHash, owner)
	if err != nil {
		t.Fatalf("RevokeKey failed: %v", err)
	}

	// Key should no longer be retrievable
	_, err = r.GetPublicKey(keyHash)
	if err != ErrKeyRevoked {
		t.Errorf("Expected ErrKeyRevoked, got %v", err)
	}

	// HasKey should return false
	if r.HasKey(keyHash) {
		t.Error("HasKey should return false for revoked key")
	}
}

func TestRevokeKeyNotOwner(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1111111111111111111111111111111111111111")
	notOwner := types.HexToAddress("0x2222222222222222222222222222222222222222")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	keyHash, _ := r.RegisterKey(pk.Bytes(), AlgoFalcon512, owner)

	// Non-owner should not be able to revoke
	err := r.RevokeKey(keyHash, notOwner)
	if err != ErrNotKeyOwner {
		t.Errorf("Expected ErrNotKeyOwner, got %v", err)
	}
}

func TestRevokeKeyNotFound(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	err := r.RevokeKey(types.Hash{}, owner)
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound, got %v", err)
	}
}

func TestRevokeKeyAlreadyRevoked(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	keyHash, _ := r.RegisterKey(pk.Bytes(), AlgoFalcon512, owner)

	// First revocation
	r.RevokeKey(keyHash, owner)

	// Second revocation should fail
	err := r.RevokeKey(keyHash, owner)
	if err != ErrKeyRevoked {
		t.Errorf("Expected ErrKeyRevoked, got %v", err)
	}
}

func TestGetKeyHashByAddress(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	expectedHash, _ := r.RegisterKey(pk.Bytes(), AlgoFalcon512, owner)

	keyHash, exists := r.GetKeyHashByAddress(owner)
	if !exists {
		t.Error("GetKeyHashByAddress should return true")
	}
	if keyHash != expectedHash {
		t.Error("Key hash doesn't match")
	}

	// Non-existent address
	_, exists = r.GetKeyHashByAddress(types.HexToAddress("0x9999999999999999999999999999999999999999"))
	if exists {
		t.Error("GetKeyHashByAddress should return false for unknown address")
	}
}

func TestGetKeyByAddress(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	pubKey := pk.Bytes()
	r.RegisterKey(pubKey, AlgoFalcon512, owner)

	retrieved, err := r.GetKeyByAddress(owner)
	if err != nil {
		t.Fatalf("GetKeyByAddress failed: %v", err)
	}
	if !bytes.Equal(retrieved, pubKey) {
		t.Error("Retrieved key doesn't match")
	}

	// Non-existent address
	_, err = r.GetKeyByAddress(types.HexToAddress("0x9999999999999999999999999999999999999999"))
	if err != ErrKeyNotFound {
		t.Errorf("Expected ErrKeyNotFound, got %v", err)
	}
}

func TestHasKey(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	keyHash, _ := r.RegisterKey(pk.Bytes(), AlgoFalcon512, owner)

	if !r.HasKey(keyHash) {
		t.Error("HasKey should return true for registered key")
	}

	if r.HasKey(types.Hash{}) {
		t.Error("HasKey should return false for non-existent key")
	}
}

func TestHasKeyForAddress(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	r.RegisterKey(pk.Bytes(), AlgoFalcon512, owner)

	if !r.HasKeyForAddress(owner) {
		t.Error("HasKeyForAddress should return true")
	}

	if r.HasKeyForAddress(types.HexToAddress("0x9999999999999999999999999999999999999999")) {
		t.Error("HasKeyForAddress should return false for unknown address")
	}
}

func TestGetAlgorithm(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	keyHash, _ := r.RegisterKey(pk.Bytes(), AlgoFalcon512, owner)

	algo, err := r.GetAlgorithm(keyHash)
	if err != nil {
		t.Fatalf("GetAlgorithm failed: %v", err)
	}
	if algo != AlgoFalcon512 {
		t.Errorf("Algorithm = %d, want %d", algo, AlgoFalcon512)
	}
}

func TestGetKeyOwner(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	keyHash, _ := r.RegisterKey(pk.Bytes(), AlgoFalcon512, owner)

	retrievedOwner, err := r.GetKeyOwner(keyHash)
	if err != nil {
		t.Fatalf("GetKeyOwner failed: %v", err)
	}
	if retrievedOwner != owner {
		t.Error("Owner doesn't match")
	}
}

func TestGetKeyData(t *testing.T) {
	r := NewRegistry()
	owner := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pk, _, _ := falcon.GenerateKey(rand.Reader)
	pubKey := pk.Bytes()
	keyHash, _ := r.RegisterKey(pubKey, AlgoFalcon512, owner)

	keyData, err := r.GetKeyData(keyHash)
	if err != nil {
		t.Fatalf("GetKeyData failed: %v", err)
	}

	if !bytes.Equal(keyData.PubKey, pubKey) {
		t.Error("PubKey doesn't match")
	}
	if keyData.Owner != owner {
		t.Error("Owner doesn't match")
	}
	if keyData.Algorithm != AlgoFalcon512 {
		t.Error("Algorithm doesn't match")
	}
	if keyData.Revoked {
		t.Error("Key should not be revoked")
	}
}

func TestGetExpectedKeySize(t *testing.T) {
	tests := []struct {
		algo uint8
		size int
	}{
		{AlgoFalcon512, Falcon512PubKeySize},
		{AlgoSQIsign, SQIsignPubKeySize},
		{AlgoDilithium2, Dilithium2PubKeySize},
		{AlgoDilithium3, Dilithium3PubKeySize},
		{99, 0}, // Unknown
	}

	for _, tt := range tests {
		size := GetExpectedKeySize(tt.algo)
		if size != tt.size {
			t.Errorf("GetExpectedKeySize(%d) = %d, want %d", tt.algo, size, tt.size)
		}
	}
}

func TestGetAlgorithmName(t *testing.T) {
	tests := []struct {
		algo uint8
		name string
	}{
		{AlgoFalcon512, "Falcon-512"},
		{AlgoSQIsign, "SQIsign-I"},
		{AlgoDilithium2, "Dilithium2"},
		{AlgoDilithium3, "Dilithium3"},
		{99, "Unknown"},
	}

	for _, tt := range tests {
		name := GetAlgorithmName(tt.algo)
		if name != tt.name {
			t.Errorf("GetAlgorithmName(%d) = %s, want %s", tt.algo, name, tt.name)
		}
	}
}

func TestComputeKeyHash(t *testing.T) {
	pubKey := []byte("test public key data")
	hash1 := ComputeKeyHash(pubKey)
	hash2 := ComputeKeyHash(pubKey)

	if hash1 != hash2 {
		t.Error("ComputeKeyHash should be deterministic")
	}

	if hash1 == (types.Hash{}) {
		t.Error("Hash should not be zero")
	}
}

func TestValidatePublicKey(t *testing.T) {
	// Valid Falcon key
	pk, _, _ := falcon.GenerateKey(rand.Reader)
	err := ValidatePublicKey(pk.Bytes(), AlgoFalcon512)
	if err != nil {
		t.Errorf("ValidatePublicKey failed for valid key: %v", err)
	}

	// Invalid algorithm
	err = ValidatePublicKey(pk.Bytes(), 99)
	if err != ErrInvalidAlgorithm {
		t.Errorf("Expected ErrInvalidAlgorithm, got %v", err)
	}

	// Invalid size
	err = ValidatePublicKey([]byte("short"), AlgoFalcon512)
	if err != ErrInvalidPubKeySize {
		t.Errorf("Expected ErrInvalidPubKeySize, got %v", err)
	}
}

func TestGlobalRegistry(t *testing.T) {
	r := GetGlobalRegistry()
	if r == nil {
		t.Fatal("GetGlobalRegistry returned nil")
	}

	r2 := GetGlobalRegistry()
	if r != r2 {
		t.Error("GetGlobalRegistry should return same instance")
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	done := make(chan bool)

	// Concurrent registrations
	for i := 0; i < 10; i++ {
		go func(idx int) {
			pk, _, _ := falcon.GenerateKey(rand.Reader)
			owner := types.HexToAddress("0x1234567890123456789012345678901234567890")
			owner[19] = byte(idx)
			r.RegisterKey(pk.Bytes(), AlgoFalcon512, owner)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Concurrent reads
	for i := 0; i < 10; i++ {
		go func(idx int) {
			owner := types.HexToAddress("0x1234567890123456789012345678901234567890")
			owner[19] = byte(idx)
			r.HasKeyForAddress(owner)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
