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
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// BlobTx Tests
// =============================================================================

func TestBlobTxType(t *testing.T) {
	tx := &BlobTx{}
	if tx.txType() != BlobTxType {
		t.Errorf("BlobTx type: expected %d, got %d", BlobTxType, tx.txType())
	}
}

func TestBlobTxTypeValue(t *testing.T) {
	if BlobTxType != 0x03 {
		t.Errorf("BlobTxType: expected 0x03, got 0x%02x", BlobTxType)
	}
}

func TestBlobTxCopy(t *testing.T) {
	original := &BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      100,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(2000000000),
		Gas:        21000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(1000000000000000000),
		Data:       []byte{0x01, 0x02, 0x03},
		BlobFeeCap: uint256.NewInt(1000),
		BlobHashes: []types.Hash{
			types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001"),
			types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000002"),
		},
		V: uint256.NewInt(0),
		R: uint256.NewInt(1),
		S: uint256.NewInt(2),
	}

	copied := original.copy().(*BlobTx)

	// Verify values are equal
	if copied.Nonce != original.Nonce {
		t.Errorf("Nonce mismatch: expected %d, got %d", original.Nonce, copied.Nonce)
	}
	if copied.Gas != original.Gas {
		t.Errorf("Gas mismatch: expected %d, got %d", original.Gas, copied.Gas)
	}
	if copied.To != original.To {
		t.Errorf("To mismatch")
	}
	if len(copied.BlobHashes) != len(original.BlobHashes) {
		t.Errorf("BlobHashes length mismatch")
	}

	// Verify deep copy (modifying copy shouldn't affect original)
	copied.Nonce = 200
	if original.Nonce == copied.Nonce {
		t.Error("Copy is not deep - modifying copy affected original")
	}
}

func TestBlobTxBlobGas(t *testing.T) {
	tests := []struct {
		name       string
		blobCount  int
		expectGas  uint64
	}{
		{"no blobs", 0, 0},
		{"one blob", 1, BlobTxBlobGasPerBlob},
		{"two blobs", 2, 2 * BlobTxBlobGasPerBlob},
		{"max blobs", 6, 6 * BlobTxBlobGasPerBlob},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := &BlobTx{
				BlobHashes: make([]types.Hash, tt.blobCount),
			}
			if got := tx.BlobGas(); got != tt.expectGas {
				t.Errorf("BlobGas() = %d, want %d", got, tt.expectGas)
			}
		})
	}
}

// =============================================================================
// Blob Constants Tests
// =============================================================================

func TestBlobConstants(t *testing.T) {
	// BlobTxBlobGasPerBlob should be 131072 (2^17)
	if BlobTxBlobGasPerBlob != 131072 {
		t.Errorf("BlobTxBlobGasPerBlob: expected 131072, got %d", BlobTxBlobGasPerBlob)
	}

	// MaxBlobGasPerBlock should be 6 * BlobTxBlobGasPerBlob
	if MaxBlobGasPerBlock != 6*BlobTxBlobGasPerBlob {
		t.Errorf("MaxBlobGasPerBlock: expected %d, got %d", 6*BlobTxBlobGasPerBlob, MaxBlobGasPerBlock)
	}

	// BlobSize should be 128KB
	if BlobSize != 131072 {
		t.Errorf("BlobSize: expected 131072, got %d", BlobSize)
	}

	// FieldElementsPerBlob should be 4096
	if FieldElementsPerBlob != 4096 {
		t.Errorf("FieldElementsPerBlob: expected 4096, got %d", FieldElementsPerBlob)
	}
}

// =============================================================================
// Blob Gas Price Tests
// =============================================================================

func TestCalcBlobFee(t *testing.T) {
	tests := []struct {
		name          string
		excessBlobGas uint64
		expectMin     uint64
	}{
		{"zero excess", 0, 1},                                  // Minimum is 1 wei
		{"small excess", 100000, 1},                           // Still minimum
		{"target excess", BlobTxTargetBlobGasPerBlock, 1},     // At target
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fee := CalcBlobFee(tt.excessBlobGas)
			if fee.Uint64() < tt.expectMin {
				t.Errorf("CalcBlobFee(%d) = %d, want >= %d", tt.excessBlobGas, fee.Uint64(), tt.expectMin)
			}
		})
	}
}

func TestCalcExcessBlobGas(t *testing.T) {
	tests := []struct {
		name             string
		parentExcess     uint64
		parentBlobGasUsed uint64
		expectExcess     uint64
	}{
		{"zero excess zero used", 0, 0, 0},
		{"zero excess below target", 0, BlobTxTargetBlobGasPerBlock - 1, 0},
		{"zero excess at target", 0, BlobTxTargetBlobGasPerBlock, 0},
		{"zero excess above target", 0, BlobTxTargetBlobGasPerBlock + 100, 100},
		{"with excess below target", 1000, BlobTxTargetBlobGasPerBlock - 1000, 0},
		{"with excess at target", 1000, BlobTxTargetBlobGasPerBlock, 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalcExcessBlobGas(tt.parentExcess, tt.parentBlobGasUsed)
			if got != tt.expectExcess {
				t.Errorf("CalcExcessBlobGas(%d, %d) = %d, want %d",
					tt.parentExcess, tt.parentBlobGasUsed, got, tt.expectExcess)
			}
		})
	}
}

// =============================================================================
// Versioned Hash Tests
// =============================================================================

func TestKZGToVersionedHash(t *testing.T) {
	commitment := Commitment{}
	for i := range commitment {
		commitment[i] = byte(i)
	}

	hash := KZGToVersionedHash(commitment)

	// First byte should be version
	if hash[0] != VersionedHashVersionKZG {
		t.Errorf("Version byte: expected 0x%02x, got 0x%02x", VersionedHashVersionKZG, hash[0])
	}
}

func TestIsValidVersionedHash(t *testing.T) {
	tests := []struct {
		name  string
		hash  types.Hash
		valid bool
	}{
		{
			"valid KZG hash",
			types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001"),
			true,
		},
		{
			"invalid version 0x00",
			types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
			false,
		},
		{
			"invalid version 0x02",
			types.HexToHash("0x0200000000000000000000000000000000000000000000000000000000000001"),
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidVersionedHash(tt.hash); got != tt.valid {
				t.Errorf("IsValidVersionedHash() = %v, want %v", got, tt.valid)
			}
		})
	}
}

// =============================================================================
// BlobTxSidecar Tests
// =============================================================================

func TestBlobTxSidecarCopy(t *testing.T) {
	original := &BlobTxSidecar{
		Blobs:       make([]Blob, 2),
		Commitments: make([]Commitment, 2),
		Proofs:      make([]Proof, 2),
	}

	// Fill with test data
	original.Blobs[0][0] = 0x01
	original.Commitments[0][0] = 0x02
	original.Proofs[0][0] = 0x03

	copied := original.Copy()

	// Verify copy
	if len(copied.Blobs) != len(original.Blobs) {
		t.Error("Blobs count mismatch")
	}
	if copied.Blobs[0][0] != original.Blobs[0][0] {
		t.Error("Blob data mismatch")
	}

	// Verify deep copy
	copied.Blobs[0][0] = 0xFF
	if original.Blobs[0][0] == copied.Blobs[0][0] {
		t.Error("Copy is not deep")
	}
}

func TestBlobTxSidecarBlobCount(t *testing.T) {
	tests := []struct {
		name     string
		blobs    int
		expected int
	}{
		{"nil sidecar", 0, 0},
		{"one blob", 1, 1},
		{"max blobs", 6, 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sidecar *BlobTxSidecar
			if tt.blobs > 0 {
				sidecar = &BlobTxSidecar{
					Blobs: make([]Blob, tt.blobs),
				}
			}
			
			var got int
			if sidecar != nil {
				got = sidecar.BlobCount()
			}
			
			if got != tt.expected {
				t.Errorf("BlobCount() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestBlobTxSidecarBlobGas(t *testing.T) {
	sidecar := &BlobTxSidecar{
		Blobs: make([]Blob, 3),
	}

	expectedGas := uint64(3) * BlobTxBlobGasPerBlob
	if got := sidecar.BlobGas(); got != expectedGas {
		t.Errorf("BlobGas() = %d, want %d", got, expectedGas)
	}
}

// =============================================================================
// Error Tests
// =============================================================================

func TestBlobErrors(t *testing.T) {
	errors := []error{
		ErrBlobGasLimitExceeded,
		ErrBlobFeeCapTooLow,
		ErrTooManyBlobs,
		ErrNoBlobs,
		ErrBlobHashMismatch,
		ErrInvalidBlobProof,
		ErrBlobTxCreate,
		ErrBlobSidecarMissing,
	}

	for _, err := range errors {
		if err.Error() == "" {
			t.Errorf("Error should have non-empty message")
		}
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkCalcBlobFee(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalcBlobFee(BlobTxTargetBlobGasPerBlock)
	}
}

func BenchmarkCalcExcessBlobGas(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalcExcessBlobGas(100000, BlobTxTargetBlobGasPerBlock)
	}
}

func BenchmarkKZGToVersionedHash(b *testing.B) {
	commitment := Commitment{}
	for i := range commitment {
		commitment[i] = byte(i)
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		KZGToVersionedHash(commitment)
	}
}

func BenchmarkBlobTxCopy(b *testing.B) {
	tx := &BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      100,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(2000000000),
		Gas:        21000,
		To:         types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Value:      uint256.NewInt(1000000000000000000),
		Data:       make([]byte, 1000),
		BlobFeeCap: uint256.NewInt(1000),
		BlobHashes: make([]types.Hash, 6),
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(2),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx.copy()
	}
}

func BenchmarkBlobTxSidecarCopy(b *testing.B) {
	sidecar := &BlobTxSidecar{
		Blobs:       make([]Blob, 6),
		Commitments: make([]Commitment, 6),
		Proofs:      make([]Proof, 6),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sidecar.Copy()
	}
}

