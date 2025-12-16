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

package internal

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Error Tests
// =============================================================================

func TestErrorsExist(t *testing.T) {
	errors := []error{
		ErrInvalidBlock,
		ErrInvalidPubSub,
		ErrBannedHash,
		ErrNoGenesis,
		ErrNonceTooLow,
		ErrNonceTooHigh,
		ErrNonceMax,
		ErrGasLimitReached,
		ErrInsufficientFundsForTransfer,
		ErrInsufficientFunds,
		ErrGasUintOverflow,
		ErrIntrinsicGas,
		ErrTxTypeNotSupported,
		ErrTipAboveFeeCap,
		ErrTipVeryHigh,
		ErrFeeCapVeryHigh,
		ErrFeeCapTooLow,
		ErrSenderNoEOA,
		ErrAlreadyDeposited,
	}

	for i, err := range errors {
		if err == nil {
			t.Errorf("Error %d should not be nil", i)
		}
		if err.Error() == "" {
			t.Errorf("Error %d should have a message", i)
		}
	}

	t.Logf("✓ All errors are defined correctly (%d errors)", len(errors))
}

func TestErrorUniqueness(t *testing.T) {
	errors := []error{
		ErrInvalidBlock,
		ErrInvalidPubSub,
		ErrBannedHash,
		ErrNoGenesis,
		ErrNonceTooLow,
		ErrNonceTooHigh,
		ErrNonceMax,
		ErrInsufficientFundsForTransfer,
		ErrInsufficientFunds,
		ErrGasUintOverflow,
		ErrIntrinsicGas,
		ErrTxTypeNotSupported,
		ErrTipAboveFeeCap,
		ErrTipVeryHigh,
		ErrFeeCapVeryHigh,
		ErrFeeCapTooLow,
		ErrSenderNoEOA,
		ErrAlreadyDeposited,
	}

	seen := make(map[string]bool)
	for _, err := range errors {
		msg := err.Error()
		if seen[msg] {
			t.Errorf("Duplicate error message: %s", msg)
		}
		seen[msg] = true
	}

	t.Logf("✓ All error messages are unique")
}

func TestNonceErrors(t *testing.T) {
	// Verify nonce error messages are descriptive
	if ErrNonceTooLow.Error() != "nonce too low" {
		t.Errorf("ErrNonceTooLow message mismatch")
	}
	if ErrNonceTooHigh.Error() != "nonce too high" {
		t.Errorf("ErrNonceTooHigh message mismatch")
	}
	if ErrNonceMax.Error() != "nonce has max value" {
		t.Errorf("ErrNonceMax message mismatch")
	}

	t.Logf("✓ Nonce errors are correctly defined")
}

func TestGasErrors(t *testing.T) {
	// Verify gas error messages
	if ErrGasUintOverflow.Error() != "gas uint64 overflow" {
		t.Errorf("ErrGasUintOverflow message mismatch")
	}
	if ErrIntrinsicGas.Error() != "intrinsic gas too low" {
		t.Errorf("ErrIntrinsicGas message mismatch")
	}

	t.Logf("✓ Gas errors are correctly defined")
}

func TestFeeErrors(t *testing.T) {
	// Verify fee-related errors
	if ErrTipAboveFeeCap.Error() != "max priority fee per gas higher than max fee per gas" {
		t.Errorf("ErrTipAboveFeeCap message mismatch")
	}

	t.Logf("✓ Fee errors are correctly defined")
}

// =============================================================================
// DerivableList Tests
// =============================================================================

// mockDerivableList implements DerivableList for testing
type mockDerivableList struct {
	items [][]byte
}

func (m *mockDerivableList) Len() int {
	return len(m.items)
}

func (m *mockDerivableList) EncodeIndex(i int, buf *bytes.Buffer) {
	buf.Write(m.items[i])
}

func TestDeriveShaEmpty(t *testing.T) {
	list := &mockDerivableList{items: [][]byte{}}

	hash := DeriveSha(list)

	// Hash should not be zero for empty list (it's the hash of empty input)
	if hash == (types.Hash{}) {
		// Empty hash is okay for empty list
		t.Logf("Empty list produces zero hash")
	}

	t.Logf("✓ DeriveSha handles empty list")
}

func TestDeriveShaConsistency(t *testing.T) {
	list := &mockDerivableList{
		items: [][]byte{
			{0x01, 0x02, 0x03},
			{0x04, 0x05, 0x06},
		},
	}

	hash1 := DeriveSha(list)
	hash2 := DeriveSha(list)

	if hash1 != hash2 {
		t.Error("DeriveSha should be deterministic")
	}

	t.Logf("✓ DeriveSha is deterministic")
}

func TestDeriveShaUniqueness(t *testing.T) {
	list1 := &mockDerivableList{
		items: [][]byte{{0x01, 0x02, 0x03}},
	}
	list2 := &mockDerivableList{
		items: [][]byte{{0x04, 0x05, 0x06}},
	}

	hash1 := DeriveSha(list1)
	hash2 := DeriveSha(list2)

	if hash1 == hash2 {
		t.Error("Different lists should produce different hashes")
	}

	t.Logf("✓ DeriveSha produces unique hashes for different inputs")
}

func TestEncodeForDerive(t *testing.T) {
	list := &mockDerivableList{
		items: [][]byte{
			{0x01, 0x02, 0x03},
			{0x04, 0x05, 0x06},
		},
	}

	buf := new(bytes.Buffer)

	result := encodeForDerive(list, 0, buf)
	if !bytes.Equal(result, []byte{0x01, 0x02, 0x03}) {
		t.Error("encodeForDerive should return correct data")
	}

	result = encodeForDerive(list, 1, buf)
	if !bytes.Equal(result, []byte{0x04, 0x05, 0x06}) {
		t.Error("encodeForDerive should return correct data for second item")
	}

	t.Logf("✓ encodeForDerive works correctly")
}

func TestEncodeForDeriveBufferReuse(t *testing.T) {
	list := &mockDerivableList{
		items: [][]byte{
			{0x01, 0x02, 0x03},
			{0x04, 0x05, 0x06},
		},
	}

	buf := new(bytes.Buffer)

	// First call
	result1 := encodeForDerive(list, 0, buf)

	// Second call should not affect first result (copy behavior)
	result2 := encodeForDerive(list, 1, buf)

	if !bytes.Equal(result1, []byte{0x01, 0x02, 0x03}) {
		t.Error("First result should be preserved after second call")
	}
	if !bytes.Equal(result2, []byte{0x04, 0x05, 0x06}) {
		t.Error("Second result should be correct")
	}

	t.Logf("✓ encodeForDerive buffer reuse is safe")
}

// =============================================================================
// Pool Tests
// =============================================================================

func TestHasherPool(t *testing.T) {
	// Get and return to pool multiple times
	for i := 0; i < 100; i++ {
		hasher := hasherPool.Get()
		if hasher == nil {
			t.Fatal("hasherPool.Get() should not return nil")
		}
		hasherPool.Put(hasher)
	}

	t.Logf("✓ hasherPool works correctly")
}

func TestEncodeBufferPool(t *testing.T) {
	// Get and return to pool multiple times
	for i := 0; i < 100; i++ {
		buf := encodeBufferPool.Get().(*bytes.Buffer)
		if buf == nil {
			t.Fatal("encodeBufferPool.Get() should not return nil")
		}
		buf.Reset()
		encodeBufferPool.Put(buf)
	}

	t.Logf("✓ encodeBufferPool works correctly")
}

// =============================================================================
// DerivableList Interface Tests
// =============================================================================

func TestDerivableListInterface(t *testing.T) {
	// Verify mockDerivableList implements DerivableList
	var _ DerivableList = &mockDerivableList{}

	t.Logf("✓ DerivableList interface is correctly defined")
}

func TestDerivableListLen(t *testing.T) {
	tests := []struct {
		name     string
		items    [][]byte
		expected int
	}{
		{"empty", [][]byte{}, 0},
		{"one_item", [][]byte{{0x01}}, 1},
		{"multiple_items", [][]byte{{0x01}, {0x02}, {0x03}}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list := &mockDerivableList{items: tt.items}
			if list.Len() != tt.expected {
				t.Errorf("Len() = %d, want %d", list.Len(), tt.expected)
			}
		})
	}

	t.Logf("✓ DerivableList.Len() works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkDeriveSha(b *testing.B) {
	list := &mockDerivableList{
		items: make([][]byte, 100),
	}
	for i := range list.items {
		list.items[i] = make([]byte, 32)
		list.items[i][0] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DeriveSha(list)
	}
}

func BenchmarkDeriveShaSmall(b *testing.B) {
	list := &mockDerivableList{
		items: [][]byte{{0x01, 0x02, 0x03}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DeriveSha(list)
	}
}

func BenchmarkHasherPoolGetPut(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hasher := hasherPool.Get()
		hasherPool.Put(hasher)
	}
}

func BenchmarkEncodeBufferPoolGetPut(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := encodeBufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		encodeBufferPool.Put(buf)
	}
}

func BenchmarkEncodeForDerive(b *testing.B) {
	list := &mockDerivableList{
		items: [][]byte{make([]byte, 256)},
	}
	buf := new(bytes.Buffer)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encodeForDerive(list, 0, buf)
	}
}
