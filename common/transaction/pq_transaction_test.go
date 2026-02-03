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
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

func TestPostQuantumTxType(t *testing.T) {
	tx := &PostQuantumTx{}
	if tx.txType() != PostQuantumTxType {
		t.Errorf("txType() = %d, want %d", tx.txType(), PostQuantumTxType)
	}
	if tx.txType() != 0x05 {
		t.Errorf("PostQuantumTxType = %d, want 0x05", tx.txType())
	}
}

func TestPostQuantumTxAccessors(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	chainID := uint256.NewInt(1)
	gasTipCap := uint256.NewInt(1000000000)
	gasFeeCap := uint256.NewInt(2000000000)
	value := uint256.NewInt(1000000000000000000)

	tx := &PostQuantumTx{
		ChainID:     chainID,
		Nonce:       42,
		GasTipCap:   gasTipCap,
		GasFeeCap:   gasFeeCap,
		Gas:         21000,
		To:          &to,
		Value:       value,
		Data:        []byte("test data"),
		SigAlgo:     PQAlgoFalcon512,
		PubKeyMode:  0,
		PubKeyData:  []byte("test public key"),
		PQSignature: []byte("test signature"),
	}

	if tx.chainID().Cmp(chainID) != 0 {
		t.Errorf("chainID() = %v, want %v", tx.chainID(), chainID)
	}
	if tx.nonce() != 42 {
		t.Errorf("nonce() = %d, want 42", tx.nonce())
	}
	if tx.gasTipCap().Cmp(gasTipCap) != 0 {
		t.Errorf("gasTipCap() = %v, want %v", tx.gasTipCap(), gasTipCap)
	}
	if tx.gasFeeCap().Cmp(gasFeeCap) != 0 {
		t.Errorf("gasFeeCap() = %v, want %v", tx.gasFeeCap(), gasFeeCap)
	}
	if tx.gas() != 21000 {
		t.Errorf("gas() = %d, want 21000", tx.gas())
	}
	if *tx.to() != to {
		t.Errorf("to() = %v, want %v", tx.to(), to)
	}
	if tx.value().Cmp(value) != 0 {
		t.Errorf("value() = %v, want %v", tx.value(), value)
	}
	if !bytes.Equal(tx.data(), []byte("test data")) {
		t.Errorf("data() = %v, want %v", tx.data(), []byte("test data"))
	}
	if tx.GetSigAlgo() != PQAlgoFalcon512 {
		t.Errorf("GetSigAlgo() = %d, want %d", tx.GetSigAlgo(), PQAlgoFalcon512)
	}
	if tx.GetPubKeyMode() != 0 {
		t.Errorf("GetPubKeyMode() = %d, want 0", tx.GetPubKeyMode())
	}
}

func TestPostQuantumTxCopy(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	chainID := uint256.NewInt(1)
	gasTipCap := uint256.NewInt(1000000000)
	gasFeeCap := uint256.NewInt(2000000000)
	value := uint256.NewInt(1000000000000000000)

	tx := &PostQuantumTx{
		ChainID:     chainID,
		Nonce:       42,
		GasTipCap:   gasTipCap,
		GasFeeCap:   gasFeeCap,
		Gas:         21000,
		To:          &to,
		Value:       value,
		Data:        []byte("test data"),
		SigAlgo:     PQAlgoFalcon512,
		PubKeyMode:  0,
		PubKeyData:  []byte("test public key"),
		PQSignature: []byte("test signature"),
		V:           uint256.NewInt(1),
		R:           uint256.NewInt(2),
		S:           uint256.NewInt(3),
	}

	cpy := tx.copy().(*PostQuantumTx)

	// Verify all fields are copied correctly
	if cpy.ChainID.Cmp(tx.ChainID) != 0 {
		t.Error("ChainID not copied correctly")
	}
	if cpy.Nonce != tx.Nonce {
		t.Error("Nonce not copied correctly")
	}
	if cpy.GasTipCap.Cmp(tx.GasTipCap) != 0 {
		t.Error("GasTipCap not copied correctly")
	}
	if cpy.GasFeeCap.Cmp(tx.GasFeeCap) != 0 {
		t.Error("GasFeeCap not copied correctly")
	}
	if cpy.Gas != tx.Gas {
		t.Error("Gas not copied correctly")
	}
	if *cpy.To != *tx.To {
		t.Error("To not copied correctly")
	}
	if cpy.Value.Cmp(tx.Value) != 0 {
		t.Error("Value not copied correctly")
	}
	if !bytes.Equal(cpy.Data, tx.Data) {
		t.Error("Data not copied correctly")
	}
	if cpy.SigAlgo != tx.SigAlgo {
		t.Error("SigAlgo not copied correctly")
	}
	if cpy.PubKeyMode != tx.PubKeyMode {
		t.Error("PubKeyMode not copied correctly")
	}
	if !bytes.Equal(cpy.PubKeyData, tx.PubKeyData) {
		t.Error("PubKeyData not copied correctly")
	}
	if !bytes.Equal(cpy.PQSignature, tx.PQSignature) {
		t.Error("PQSignature not copied correctly")
	}

	// Verify deep copy - modifying copy doesn't affect original
	cpy.Nonce = 999
	if tx.Nonce == 999 {
		t.Error("Copy is not deep - modifying copy affected original")
	}

	cpy.PubKeyData[0] = 0xFF
	if tx.PubKeyData[0] == 0xFF {
		t.Error("PubKeyData is not deep copied")
	}
}

func TestPostQuantumTxSigAlgoName(t *testing.T) {
	tests := []struct {
		algo uint8
		name string
	}{
		{PQAlgoFalcon512, "Falcon-512"},
		{PQAlgoSQIsign, "SQIsign-I"},
		{PQAlgoDilithium2, "Dilithium2"},
		{PQAlgoDilithium3, "Dilithium3"},
		{255, "Unknown"},
	}

	for _, tt := range tests {
		tx := &PostQuantumTx{SigAlgo: tt.algo}
		if got := tx.GetSigAlgoName(); got != tt.name {
			t.Errorf("GetSigAlgoName() for algo %d = %s, want %s", tt.algo, got, tt.name)
		}
	}
}

func TestPostQuantumTxExpectedSizes(t *testing.T) {
	tests := []struct {
		algo          uint8
		pubKeySize    int
		signatureSize int
	}{
		{PQAlgoFalcon512, Falcon512PublicKeySize, Falcon512SignatureSize},
		{PQAlgoSQIsign, SQIsignPublicKeySize, SQIsignSignatureSize},
		{PQAlgoDilithium2, Dilithium2PublicKeySize, Dilithium2SignatureSize},
		{PQAlgoDilithium3, Dilithium3PublicKeySize, Dilithium3SignatureSize},
	}

	for _, tt := range tests {
		tx := &PostQuantumTx{SigAlgo: tt.algo}
		if got := tx.GetExpectedPubKeySize(); got != tt.pubKeySize {
			t.Errorf("GetExpectedPubKeySize() for algo %d = %d, want %d", tt.algo, got, tt.pubKeySize)
		}
		if got := tx.GetExpectedSignatureSize(); got != tt.signatureSize {
			t.Errorf("GetExpectedSignatureSize() for algo %d = %d, want %d", tt.algo, got, tt.signatureSize)
		}
	}
}

func TestPostQuantumTxPubKeyModes(t *testing.T) {
	// Test full public key mode
	tx := &PostQuantumTx{
		PubKeyMode: 0,
		PubKeyData: make([]byte, Falcon512PublicKeySize),
	}
	if !tx.IsFullPubKey() {
		t.Error("IsFullPubKey() should return true for PubKeyMode 0")
	}

	// Test hash reference mode
	tx.PubKeyMode = 1
	tx.PubKeyData = make([]byte, PQPublicKeyHashSize)
	if tx.IsFullPubKey() {
		t.Error("IsFullPubKey() should return false for PubKeyMode 1")
	}
}

func TestPostQuantumTxSetPubKey(t *testing.T) {
	tx := &PostQuantumTx{}
	pubKey := []byte("test public key data for testing purposes")

	// Test setting full public key
	tx.SetPubKey(pubKey, false)
	if tx.PubKeyMode != 0 {
		t.Error("SetPubKey with useHash=false should set PubKeyMode to 0")
	}
	if !bytes.Equal(tx.PubKeyData, pubKey) {
		t.Error("SetPubKey should copy the public key")
	}

	// Test setting public key hash
	tx.SetPubKey(pubKey, true)
	if tx.PubKeyMode != 1 {
		t.Error("SetPubKey with useHash=true should set PubKeyMode to 1")
	}
	if len(tx.PubKeyData) != 32 {
		t.Errorf("SetPubKey with useHash=true should produce 32-byte hash, got %d", len(tx.PubKeyData))
	}
}

func TestPostQuantumTxSetPQSignature(t *testing.T) {
	tx := &PostQuantumTx{}
	sig := []byte("test signature data")

	tx.SetPQSignature(sig)
	if !bytes.Equal(tx.PQSignature, sig) {
		t.Error("SetPQSignature should copy the signature")
	}

	// Verify it's a deep copy
	sig[0] = 0xFF
	if tx.PQSignature[0] == 0xFF {
		t.Error("SetPQSignature should make a deep copy")
	}
}

func TestPostQuantumTxHash(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := &PostQuantumTx{
		ChainID:     uint256.NewInt(1),
		Nonce:       42,
		GasTipCap:   uint256.NewInt(1000000000),
		GasFeeCap:   uint256.NewInt(2000000000),
		Gas:         21000,
		To:          &to,
		Value:       uint256.NewInt(1000000000000000000),
		Data:        []byte("test data"),
		SigAlgo:     PQAlgoFalcon512,
		PubKeyMode:  0,
		PubKeyData:  []byte("test public key"),
		PQSignature: []byte("test signature"),
	}

	hash1 := tx.hash()
	hash2 := tx.hash()

	// Hash should be deterministic
	if hash1 != hash2 {
		t.Error("hash() should return the same hash for the same transaction")
	}

	// Hash should be different from signing hash
	signingHash := tx.SigningHash()
	if hash1 == signingHash {
		t.Error("hash() should include signature, SigningHash() should not")
	}
}

func TestPostQuantumTxSigningHash(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := &PostQuantumTx{
		ChainID:     uint256.NewInt(1),
		Nonce:       42,
		GasTipCap:   uint256.NewInt(1000000000),
		GasFeeCap:   uint256.NewInt(2000000000),
		Gas:         21000,
		To:          &to,
		Value:       uint256.NewInt(1000000000000000000),
		Data:        []byte("test data"),
		SigAlgo:     PQAlgoFalcon512,
		PubKeyMode:  0,
		PubKeyData:  []byte("test public key"),
		PQSignature: []byte("test signature"),
	}

	signingHash1 := tx.SigningHash()

	// Change signature - signing hash should remain the same
	tx.PQSignature = []byte("different signature")
	signingHash2 := tx.SigningHash()

	if signingHash1 != signingHash2 {
		t.Error("SigningHash() should not include the signature")
	}
}

func TestPostQuantumTxEstimatedSize(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := &PostQuantumTx{
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
	}

	size := tx.EstimatedSize()
	// Base (200) + Falcon pubkey (897) + Falcon sig (690) + data (9) = ~1796
	if size < 1000 {
		t.Errorf("EstimatedSize() = %d, expected > 1000 for Falcon tx with full key", size)
	}

	// Test with hash reference mode
	tx.PubKeyMode = 1
	sizeWithHash := tx.EstimatedSize()
	// Should be smaller since hash (32) < full key (897)
	if sizeWithHash >= size {
		t.Error("EstimatedSize with hash reference should be smaller than full key")
	}
}

func TestNewPostQuantumTx(t *testing.T) {
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	chainID := uint256.NewInt(1)
	gasTipCap := uint256.NewInt(1000000000)
	gasFeeCap := uint256.NewInt(2000000000)
	value := uint256.NewInt(1000000000000000000)
	data := []byte("test data")

	tx := NewPostQuantumTx(
		chainID,
		42,
		&to,
		value,
		21000,
		gasTipCap,
		gasFeeCap,
		data,
		nil,
		PQAlgoFalcon512,
	)

	if tx == nil {
		t.Fatal("NewPostQuantumTx returned nil")
	}
	if tx.ChainID.Cmp(chainID) != 0 {
		t.Error("ChainID not set correctly")
	}
	if tx.Nonce != 42 {
		t.Error("Nonce not set correctly")
	}
	if *tx.To != to {
		t.Error("To not set correctly")
	}
	if tx.Value.Cmp(value) != 0 {
		t.Error("Value not set correctly")
	}
	if tx.Gas != 21000 {
		t.Error("Gas not set correctly")
	}
	if tx.GasTipCap.Cmp(gasTipCap) != 0 {
		t.Error("GasTipCap not set correctly")
	}
	if tx.GasFeeCap.Cmp(gasFeeCap) != 0 {
		t.Error("GasFeeCap not set correctly")
	}
	if !bytes.Equal(tx.Data, data) {
		t.Error("Data not set correctly")
	}
	if tx.SigAlgo != PQAlgoFalcon512 {
		t.Error("SigAlgo not set correctly")
	}
	if tx.PubKeyMode != 0 {
		t.Error("PubKeyMode should default to 0")
	}
}

func TestPostQuantumTxConstants(t *testing.T) {
	// Verify transaction type constant
	if PostQuantumTxType != 0x05 {
		t.Errorf("PostQuantumTxType = 0x%02x, want 0x05", PostQuantumTxType)
	}

	// Verify algorithm constants
	if PQAlgoFalcon512 != 0 {
		t.Errorf("PQAlgoFalcon512 = %d, want 0", PQAlgoFalcon512)
	}
	if PQAlgoSQIsign != 1 {
		t.Errorf("PQAlgoSQIsign = %d, want 1", PQAlgoSQIsign)
	}
	if PQAlgoDilithium2 != 2 {
		t.Errorf("PQAlgoDilithium2 = %d, want 2", PQAlgoDilithium2)
	}
	if PQAlgoDilithium3 != 3 {
		t.Errorf("PQAlgoDilithium3 = %d, want 3", PQAlgoDilithium3)
	}

	// Verify key/signature sizes
	if Falcon512PublicKeySize != 897 {
		t.Errorf("Falcon512PublicKeySize = %d, want 897", Falcon512PublicKeySize)
	}
	if Falcon512SignatureSize != 690 {
		t.Errorf("Falcon512SignatureSize = %d, want 690", Falcon512SignatureSize)
	}
	if SQIsignPublicKeySize != 64 {
		t.Errorf("SQIsignPublicKeySize = %d, want 64", SQIsignPublicKeySize)
	}
	if SQIsignSignatureSize != 177 {
		t.Errorf("SQIsignSignatureSize = %d, want 177", SQIsignSignatureSize)
	}
	if Dilithium2PublicKeySize != 1312 {
		t.Errorf("Dilithium2PublicKeySize = %d, want 1312", Dilithium2PublicKeySize)
	}
	if Dilithium2SignatureSize != 2420 {
		t.Errorf("Dilithium2SignatureSize = %d, want 2420", Dilithium2SignatureSize)
	}
}

func TestTransactionIsPostQuantum(t *testing.T) {
	// Test PQ transaction
	pqInner := &PostQuantumTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     42,
		GasTipCap: uint256.NewInt(1000000000),
		GasFeeCap: uint256.NewInt(2000000000),
		Gas:       21000,
		Value:     uint256.NewInt(0),
		V:         uint256.NewInt(0),
		R:         uint256.NewInt(0),
		S:         uint256.NewInt(0),
	}
	pqTx := NewTx(pqInner)
	if !pqTx.IsPostQuantum() {
		t.Error("IsPostQuantum() should return true for PQ transaction")
	}

	// Test non-PQ transaction
	legacyInner := &LegacyTx{
		Nonce:    42,
		GasPrice: uint256.NewInt(1000000000),
		Gas:      21000,
		Value:    uint256.NewInt(0),
		V:        uint256.NewInt(0),
		R:        uint256.NewInt(0),
		S:        uint256.NewInt(0),
	}
	legacyTx := NewTx(legacyInner)
	if legacyTx.IsPostQuantum() {
		t.Error("IsPostQuantum() should return false for legacy transaction")
	}
}

func TestTransactionGetPostQuantumTx(t *testing.T) {
	pqInner := &PostQuantumTx{
		ChainID:     uint256.NewInt(1),
		Nonce:       42,
		GasTipCap:   uint256.NewInt(1000000000),
		GasFeeCap:   uint256.NewInt(2000000000),
		Gas:         21000,
		Value:       uint256.NewInt(0),
		SigAlgo:     PQAlgoFalcon512,
		PubKeyData:  []byte("test key"),
		PQSignature: []byte("test sig"),
		V:           uint256.NewInt(0),
		R:           uint256.NewInt(0),
		S:           uint256.NewInt(0),
	}
	tx := NewTx(pqInner)

	pqTx := tx.GetPostQuantumTx()
	if pqTx == nil {
		t.Fatal("GetPostQuantumTx() returned nil for PQ transaction")
	}
	if pqTx.SigAlgo != PQAlgoFalcon512 {
		t.Error("GetPostQuantumTx() returned incorrect SigAlgo")
	}

	// Test PQSigAlgo helper
	if tx.PQSigAlgo() != PQAlgoFalcon512 {
		t.Error("PQSigAlgo() returned incorrect value")
	}

	// Test PQSignature helper
	if !bytes.Equal(tx.PQSignature(), []byte("test sig")) {
		t.Error("PQSignature() returned incorrect value")
	}

	// Test PQPubKeyData helper
	if !bytes.Equal(tx.PQPubKeyData(), []byte("test key")) {
		t.Error("PQPubKeyData() returned incorrect value")
	}
}
