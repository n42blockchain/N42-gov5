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
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/crypto/falcon"
	"github.com/n42blockchain/N42/common/types"
)

func TestNewPostQuantumSigner(t *testing.T) {
	chainId := big.NewInt(1)
	signer := NewPostQuantumSigner(chainId)

	pqSigner, ok := signer.(PostQuantumSigner)
	if !ok {
		t.Fatal("NewPostQuantumSigner should return PostQuantumSigner")
	}

	if pqSigner.ChainID().Cmp(chainId) != 0 {
		t.Errorf("ChainID() = %v, want %v", pqSigner.ChainID(), chainId)
	}
}

func TestPostQuantumSignerEqual(t *testing.T) {
	signer1 := NewPostQuantumSigner(big.NewInt(1))
	signer2 := NewPostQuantumSigner(big.NewInt(1))
	signer3 := NewPostQuantumSigner(big.NewInt(2))
	signer4 := NewLondonSigner(big.NewInt(1))

	if !signer1.Equal(signer2) {
		t.Error("Signers with same chain ID should be equal")
	}

	if signer1.Equal(signer3) {
		t.Error("Signers with different chain IDs should not be equal")
	}

	if signer1.Equal(signer4) {
		t.Error("PostQuantumSigner should not equal LondonSigner")
	}
}

func TestPostQuantumSignerHash(t *testing.T) {
	chainId := big.NewInt(1)
	signer := NewPostQuantumSigner(chainId)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Test with PQ transaction
	pqTx := &PostQuantumTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      42,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(2000000000),
		Gas:        21000,
		To:         &to,
		Value:      uint256.NewInt(1000000000000000000),
		Data:       []byte("test data"),
		SigAlgo:    PQAlgoFalcon512,
		PubKeyMode: 0,
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(0),
		S:          uint256.NewInt(0),
	}
	tx := NewTx(pqTx)

	hash, err := signer.Hash(tx)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	// Hash should equal the SigningHash
	expectedHash := pqTx.SigningHash()
	if hash != expectedHash {
		t.Errorf("Hash() = %v, want %v", hash, expectedHash)
	}
}

func TestPostQuantumSignerSignatureValues(t *testing.T) {
	chainId := big.NewInt(1)
	signer := NewPostQuantumSigner(chainId)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pqTx := &PostQuantumTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      42,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(2000000000),
		Gas:        21000,
		To:         &to,
		Value:      uint256.NewInt(1000000000000000000),
		SigAlgo:    PQAlgoFalcon512,
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(0),
		S:          uint256.NewInt(0),
	}
	tx := NewTx(pqTx)

	r, s, v, err := signer.SignatureValues(tx, nil)
	if err != nil {
		t.Fatalf("SignatureValues failed: %v", err)
	}

	// PQ transactions should return nil for r, s, v
	if r != nil || s != nil || v != nil {
		t.Error("SignatureValues should return nil for PQ transactions")
	}
}

func TestPostQuantumSignerSenderWithFalcon(t *testing.T) {
	chainId := big.NewInt(1)
	signer := NewPostQuantumSigner(chainId)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Generate Falcon key pair
	pk, sk, err := falcon.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate Falcon key: %v", err)
	}

	// Create and sign a PQ transaction
	pqTx := NewPostQuantumTx(
		uint256.NewInt(1),
		42,
		&to,
		uint256.NewInt(1000000000000000000),
		21000,
		uint256.NewInt(1000000000),
		uint256.NewInt(2000000000),
		[]byte("test data"),
		nil,
		PQAlgoFalcon512,
	)

	// Sign the transaction
	signedTx, err := SignNewPQTx(sk, pqTx)
	if err != nil {
		t.Fatalf("Failed to sign PQ transaction: %v", err)
	}

	// Recover sender
	sender, err := signer.Sender(signedTx)
	if err != nil {
		t.Fatalf("Sender failed: %v", err)
	}

	// Compute expected address
	expectedAddr := computePQAddress(pk.Bytes())
	if sender != expectedAddr {
		t.Errorf("Sender() = %v, want %v", sender, expectedAddr)
	}
}

func TestPostQuantumSignerInvalidChainID(t *testing.T) {
	signer := NewPostQuantumSigner(big.NewInt(1))
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Generate Falcon key pair
	pk, sk, err := falcon.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate Falcon key: %v", err)
	}

	// Create transaction with different chain ID
	pqTx := NewPostQuantumTx(
		uint256.NewInt(999), // Different chain ID
		42,
		&to,
		uint256.NewInt(1000000000000000000),
		21000,
		uint256.NewInt(1000000000),
		uint256.NewInt(2000000000),
		nil,
		nil,
		PQAlgoFalcon512,
	)

	// Set public key and sign
	pqTx.SetPubKey(pk.Bytes(), false)
	signingHash := pqTx.SigningHash()
	sig, _ := falcon.Sign(sk, signingHash[:])
	pqTx.SetPQSignature(sig)

	tx := NewTx(pqTx)

	_, err = signer.Sender(tx)
	if err != ErrInvalidChainId {
		t.Errorf("Expected ErrInvalidChainId, got %v", err)
	}
}

func TestPostQuantumSignerInvalidSignature(t *testing.T) {
	chainId := big.NewInt(1)
	signer := NewPostQuantumSigner(chainId)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Generate Falcon key pair
	pk, _, err := falcon.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate Falcon key: %v", err)
	}

	// Create transaction with invalid signature
	pqTx := NewPostQuantumTx(
		uint256.NewInt(1),
		42,
		&to,
		uint256.NewInt(1000000000000000000),
		21000,
		uint256.NewInt(1000000000),
		uint256.NewInt(2000000000),
		nil,
		nil,
		PQAlgoFalcon512,
	)

	pqTx.SetPubKey(pk.Bytes(), false)
	pqTx.SetPQSignature([]byte("invalid signature data"))

	tx := NewTx(pqTx)

	_, err = signer.Sender(tx)
	if err != ErrPQSignatureVerificationFailed {
		t.Errorf("Expected ErrPQSignatureVerificationFailed, got %v", err)
	}
}

func TestPostQuantumSignerWithHashReferenceMode(t *testing.T) {
	chainId := big.NewInt(1)
	signer := NewPostQuantumSigner(chainId)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create transaction with hash reference mode (without full key)
	pqTx := &PostQuantumTx{
		ChainID:     uint256.NewInt(1),
		Nonce:       42,
		GasTipCap:   uint256.NewInt(1000000000),
		GasFeeCap:   uint256.NewInt(2000000000),
		Gas:         21000,
		To:          &to,
		Value:       uint256.NewInt(1000000000000000000),
		SigAlgo:     PQAlgoFalcon512,
		PubKeyMode:  1,                           // Hash reference mode
		PubKeyData:  make([]byte, 32),            // 32-byte hash
		PQSignature: []byte("some signature"),
		V:           uint256.NewInt(0),
		R:           uint256.NewInt(0),
		S:           uint256.NewInt(0),
	}
	tx := NewTx(pqTx)

	// Should fail because we can't verify with just a hash
	_, err := signer.Sender(tx)
	if err != ErrPQPublicKeyRequired {
		t.Errorf("Expected ErrPQPublicKeyRequired, got %v", err)
	}
}

func TestPostQuantumSignerDelegatesLegacyTx(t *testing.T) {
	chainId := big.NewInt(1)
	signer := NewPostQuantumSigner(chainId)

	// Create a legacy transaction
	legacyTx := &LegacyTx{
		Nonce:    42,
		GasPrice: uint256.NewInt(1000000000),
		Gas:      21000,
		Value:    uint256.NewInt(0),
		V:        uint256.NewInt(27),
		R:        uint256.NewInt(0),
		S:        uint256.NewInt(0),
	}
	tx := NewTx(legacyTx)

	// Hash should delegate to London signer
	hash, err := signer.Hash(tx)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	// Verify it matches London signer's hash
	londonSigner := NewLondonSigner(chainId)
	expectedHash, _ := londonSigner.Hash(tx)
	if hash != expectedHash {
		t.Error("PQ signer should delegate legacy tx hash to London signer")
	}
}

func TestSignPQTransaction(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Generate Falcon key pair
	_, sk, err := falcon.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate Falcon key: %v", err)
	}

	pqTx := NewPostQuantumTx(
		uint256.NewInt(1),
		42,
		&to,
		uint256.NewInt(1000000000000000000),
		21000,
		uint256.NewInt(1000000000),
		uint256.NewInt(2000000000),
		[]byte("test data"),
		nil,
		PQAlgoFalcon512,
	)

	err = SignPQTransaction(pqTx, sk)
	if err != nil {
		t.Fatalf("SignPQTransaction failed: %v", err)
	}

	if len(pqTx.PQSignature) == 0 {
		t.Error("Signature should be set after signing")
	}

	if len(pqTx.PubKeyData) == 0 {
		t.Error("Public key should be set after signing")
	}
}

func TestSignPQTransactionInvalidKeyType(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	pqTx := NewPostQuantumTx(
		uint256.NewInt(1),
		42,
		&to,
		uint256.NewInt(0),
		21000,
		uint256.NewInt(1000000000),
		uint256.NewInt(2000000000),
		nil,
		nil,
		PQAlgoFalcon512,
	)

	// Try signing with wrong key type
	err := SignPQTransaction(pqTx, "not a private key")
	if err == nil {
		t.Error("SignPQTransaction should fail with invalid key type")
	}
}

func TestSignPQTransactionUnsupportedAlgo(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")

	_, sk, _ := falcon.GenerateKey(rand.Reader)

	pqTx := NewPostQuantumTx(
		uint256.NewInt(1),
		42,
		&to,
		uint256.NewInt(0),
		21000,
		uint256.NewInt(1000000000),
		uint256.NewInt(2000000000),
		nil,
		nil,
		PQAlgoSQIsign, // Not yet implemented
	)

	err := SignPQTransaction(pqTx, sk)
	if err == nil {
		t.Error("SignPQTransaction should fail for unsupported algorithm")
	}
}

func TestMakeSignerWithPQ(t *testing.T) {
	chainId := big.NewInt(1)

	// With PQ enabled
	pqSigner := MakeSignerWithPQ(chainId, true)
	if _, ok := pqSigner.(PostQuantumSigner); !ok {
		t.Error("MakeSignerWithPQ(true) should return PostQuantumSigner")
	}

	// With PQ disabled
	nonPQSigner := MakeSignerWithPQ(chainId, false)
	if _, ok := nonPQSigner.(londonSigner); !ok {
		t.Error("MakeSignerWithPQ(false) should return londonSigner")
	}
}

func TestResolvePQPublicKeyFullKeyMode(t *testing.T) {
	pqTx := &PostQuantumTx{
		PubKeyMode: 0,
		PubKeyData: []byte("full public key data"),
	}

	resolved, err := ResolvePQPublicKey(pqTx, nil)
	if err != nil {
		t.Fatalf("ResolvePQPublicKey failed: %v", err)
	}

	if string(resolved) != string(pqTx.PubKeyData) {
		t.Error("ResolvePQPublicKey should return the full public key directly")
	}
}

// Helper function to compute PQ address from public key
func computePQAddress(pubKey []byte) types.Address {
	signer := NewPostQuantumSigner(big.NewInt(1)).(PostQuantumSigner)
	return signer.derivePQAddress(pubKey)
}
