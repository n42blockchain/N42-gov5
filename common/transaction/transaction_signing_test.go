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
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestDecodeSignatureValidLength(t *testing.T) {
	// Create a valid signature (65 bytes)
	sig := make([]byte, crypto.SignatureLength)
	for i := range sig {
		sig[i] = byte(i)
	}

	r, s, v, err := decodeSignature(sig)
	if err != nil {
		t.Fatalf("decodeSignature failed with valid input: %v", err)
	}

	if r == nil || s == nil || v == nil {
		t.Fatal("decodeSignature returned nil values for valid input")
	}

	// Verify r, s are properly decoded from the signature bytes
	expectedR := new(big.Int).SetBytes(sig[:32])
	expectedS := new(big.Int).SetBytes(sig[32:64])

	if r.Cmp(expectedR) != 0 {
		t.Errorf("r mismatch: got %v, want %v", r, expectedR)
	}
	if s.Cmp(expectedS) != 0 {
		t.Errorf("s mismatch: got %v, want %v", s, expectedS)
	}
}

func TestDecodeSignatureInvalidLength(t *testing.T) {
	testCases := []struct {
		name   string
		sigLen int
	}{
		{"empty signature", 0},
		{"too short (1 byte)", 1},
		{"too short (32 bytes)", 32},
		{"too short (64 bytes)", 64},
		{"too long (66 bytes)", 66},
		{"too long (100 bytes)", 100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sig := make([]byte, tc.sigLen)

			r, s, v, err := decodeSignature(sig)
			if err == nil {
				t.Errorf("decodeSignature should fail with %d byte signature", tc.sigLen)
			}
			if !errors.Is(err, ErrInvalidSignatureSize) {
				t.Errorf("expected ErrInvalidSignatureSize, got: %v", err)
			}
			if r != nil || s != nil || v != nil {
				t.Error("decodeSignature should return nil values on error")
			}
		})
	}
}

func TestMakeSignerAllowsNilInputs(t *testing.T) {
	if !MakeSigner(nil, nil).Equal(FrontierSigner{}) {
		t.Fatal("MakeSigner(nil, nil) did not return FrontierSigner")
	}

	config := &params.ChainConfig{
		ChainID:     big.NewInt(1),
		LondonBlock: big.NewInt(0),
	}
	if !MakeSigner(config, nil).Equal(NewLondonSigner(config.ChainID)) {
		t.Fatal("MakeSigner(config, nil) did not return London signer")
	}
}

func TestDecodeSignatureNoPanic(t *testing.T) {
	// This test ensures that invalid signatures no longer cause panics
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("decodeSignature panicked: %v", r)
		}
	}()

	// Test with various invalid inputs that previously would panic
	invalidInputs := [][]byte{
		nil,
		{},
		make([]byte, 10),
		make([]byte, 64),
		make([]byte, 66),
	}

	for i, sig := range invalidInputs {
		_, _, _, err := decodeSignature(sig)
		if err == nil {
			t.Errorf("test case %d: expected error for invalid signature length", i)
		}
	}
}

func TestSignatureValuesInvalidSignature(t *testing.T) {
	// Test that SignatureValues methods properly propagate decodeSignature errors
	chainID := big.NewInt(1)
	signer := NewLondonSigner(chainID)

	// Create a legacy transaction for testing
	tx := NewTx(&LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1),
		Gas:      21000,
		To:       nil,
		Value:    uint256.NewInt(0),
		Data:     nil,
	})

	// Test with invalid signature length
	invalidSig := make([]byte, 10)
	_, _, _, err := signer.SignatureValues(tx, invalidSig)
	if err == nil {
		t.Error("SignatureValues should fail with invalid signature length")
	}
	if !errors.Is(err, ErrInvalidSignatureSize) {
		t.Errorf("expected ErrInvalidSignatureSize, got: %v", err)
	}
}

func TestFrontierSignerSignatureValuesInvalidSig(t *testing.T) {
	signer := FrontierSigner{}
	tx := NewTx(&LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1),
		Gas:      21000,
	})

	invalidSig := make([]byte, 32) // wrong length
	_, _, _, err := signer.SignatureValues(tx, invalidSig)
	if err == nil {
		t.Error("FrontierSigner.SignatureValues should fail with invalid signature")
	}
}

func TestHomesteadSignerSignatureValuesInvalidSig(t *testing.T) {
	signer := HomesteadSigner{}
	tx := NewTx(&LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1),
		Gas:      21000,
	})

	invalidSig := make([]byte, 32) // wrong length
	_, _, _, err := signer.SignatureValues(tx, invalidSig)
	if err == nil {
		t.Error("HomesteadSigner.SignatureValues should fail with invalid signature")
	}
}

func TestEIP155SignerSignatureValuesInvalidSig(t *testing.T) {
	signer := NewEIP155Signer(big.NewInt(1))
	tx := NewTx(&LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1),
		Gas:      21000,
	})

	invalidSig := make([]byte, 32) // wrong length
	_, _, _, err := signer.SignatureValues(tx, invalidSig)
	if err == nil {
		t.Error("EIP155Signer.SignatureValues should fail with invalid signature")
	}
}

func TestHashReturnsErrorForUnsupportedTxType(t *testing.T) {
	// Test that Hash() returns error for unsupported transaction types
	// This verifies the fix for the silent failure issue

	chainID := big.NewInt(1)

	// Create an EIP2930 signer which only supports LegacyTxType and AccessListTxType
	signer := NewEIP2930Signer(chainID)

	// Create a DynamicFeeTx which is NOT supported by eip2930Signer
	tx := NewTx(&DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       21000,
	})

	// The eip2930Signer.Hash should return an error for DynamicFeeTx
	_, err := signer.Hash(tx)
	if err == nil {
		t.Error("eip2930Signer.Hash should return error for DynamicFeeTx")
	}
	if !errors.Is(err, ErrTxTypeNotSupported) {
		t.Errorf("expected ErrTxTypeNotSupported, got: %v", err)
	}
}

func TestHashSucceedsForSupportedTxTypes(t *testing.T) {
	chainID := big.NewInt(1)

	testCases := []struct {
		name   string
		signer Signer
		tx     *Transaction
	}{
		{
			name:   "LondonSigner with DynamicFeeTx",
			signer: NewLondonSigner(chainID),
			tx: NewTx(&DynamicFeeTx{
				ChainID:   uint256.NewInt(1),
				Nonce:     0,
				GasTipCap: uint256.NewInt(1),
				GasFeeCap: uint256.NewInt(1),
				Gas:       21000,
			}),
		},
		{
			name:   "LondonSigner with LegacyTx",
			signer: NewLondonSigner(chainID),
			tx: NewTx(&LegacyTx{
				Nonce:    0,
				GasPrice: uint256.NewInt(1),
				Gas:      21000,
			}),
		},
		{
			name:   "EIP2930Signer with LegacyTx",
			signer: NewEIP2930Signer(chainID),
			tx: NewTx(&LegacyTx{
				Nonce:    0,
				GasPrice: uint256.NewInt(1),
				Gas:      21000,
			}),
		},
		{
			name:   "EIP155Signer with LegacyTx",
			signer: NewEIP155Signer(chainID),
			tx: NewTx(&LegacyTx{
				Nonce:    0,
				GasPrice: uint256.NewInt(1),
				Gas:      21000,
			}),
		},
		{
			name:   "FrontierSigner with LegacyTx",
			signer: FrontierSigner{},
			tx: NewTx(&LegacyTx{
				Nonce:    0,
				GasPrice: uint256.NewInt(1),
				Gas:      21000,
			}),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := tc.signer.Hash(tc.tx)
			if err != nil {
				t.Errorf("Hash() returned unexpected error: %v", err)
			}
			if h == (types.Hash{}) {
				t.Error("Hash() returned empty hash")
			}
		})
	}
}
