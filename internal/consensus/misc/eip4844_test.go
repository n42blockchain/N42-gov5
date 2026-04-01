// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package misc

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestVerifyEIP4844Header_Valid(t *testing.T) {
	parentBlobGasUsed := uint64(params.BlobTxBlobGasPerBlob * 3) // 3 blobs
	parentExcessBlobGas := uint64(0)
	expectedExcess := transaction.CalcExcessBlobGas(parentExcessBlobGas, parentBlobGasUsed)

	parent := &block.Header{
		BlobGasUsed:   u64ptr(parentBlobGasUsed),
		ExcessBlobGas: u64ptr(parentExcessBlobGas),
	}
	child := &block.Header{
		BlobGasUsed:   u64ptr(params.BlobTxBlobGasPerBlob * 2), // 2 blobs
		ExcessBlobGas: u64ptr(expectedExcess),
	}

	err := VerifyEIP4844Header(parent, child, nil)
	if err != nil {
		t.Fatalf("valid header should pass verification: %v", err)
	}
}

func TestVerifyEIP4844Header_ZeroBlobs(t *testing.T) {
	parent := &block.Header{
		BlobGasUsed:   u64ptr(0),
		ExcessBlobGas: u64ptr(0),
	}
	expectedExcess := transaction.CalcExcessBlobGas(0, 0)
	child := &block.Header{
		BlobGasUsed:   u64ptr(0),
		ExcessBlobGas: u64ptr(expectedExcess),
	}

	err := VerifyEIP4844Header(parent, child, nil)
	if err != nil {
		t.Fatalf("zero-blob header should pass: %v", err)
	}
}

func TestVerifyEIP4844Header_ExcessBlobGasMismatch(t *testing.T) {
	parent := &block.Header{
		BlobGasUsed:   u64ptr(params.BlobTxBlobGasPerBlob * 3),
		ExcessBlobGas: u64ptr(0),
	}
	child := &block.Header{
		BlobGasUsed:   u64ptr(params.BlobTxBlobGasPerBlob),
		ExcessBlobGas: u64ptr(999999), // Wrong value
	}

	err := VerifyEIP4844Header(parent, child, nil)
	if err == nil {
		t.Fatal("expected error for excess blob gas mismatch")
	}
}

func TestVerifyEIP4844Header_BlobGasExceedsMax(t *testing.T) {
	parent := &block.Header{
		BlobGasUsed:   u64ptr(0),
		ExcessBlobGas: u64ptr(0),
	}
	child := &block.Header{
		BlobGasUsed:   u64ptr(params.MaxBlobGasPerBlock + params.BlobTxBlobGasPerBlob), // Exceeds max
		ExcessBlobGas: u64ptr(transaction.CalcExcessBlobGas(0, 0)),
	}

	err := VerifyEIP4844Header(parent, child, nil)
	if err == nil {
		t.Fatal("expected error for blob gas exceeding maximum")
	}
}

func TestVerifyEIP4844Header_BlobGasNotMultiple(t *testing.T) {
	parent := &block.Header{
		BlobGasUsed:   u64ptr(0),
		ExcessBlobGas: u64ptr(0),
	}
	child := &block.Header{
		BlobGasUsed:   u64ptr(params.BlobTxBlobGasPerBlob + 1), // Not a multiple
		ExcessBlobGas: u64ptr(transaction.CalcExcessBlobGas(0, 0)),
	}

	err := VerifyEIP4844Header(parent, child, nil)
	if err == nil {
		t.Fatal("expected error for blob gas not being a multiple of BlobTxBlobGasPerBlob")
	}
}

func TestVerifyEIP4844Header_MaxBlobs(t *testing.T) {
	// Exactly at the maximum should be valid.
	parent := &block.Header{
		BlobGasUsed:   u64ptr(0),
		ExcessBlobGas: u64ptr(0),
	}
	child := &block.Header{
		BlobGasUsed:   u64ptr(params.MaxBlobGasPerBlock),
		ExcessBlobGas: u64ptr(transaction.CalcExcessBlobGas(0, 0)),
	}

	err := VerifyEIP4844Header(parent, child, nil)
	if err != nil {
		t.Fatalf("max blob gas should be valid: %v", err)
	}
}

func TestVerifyEIP4844Header_ExcessAccumulation(t *testing.T) {
	// Test that excess blob gas accumulates correctly across blocks.
	// Parent had excess + used > target, so child should have nonzero excess.
	parentExcess := uint64(params.BlobTxBlobGasPerBlob * 2) // Some existing excess
	parentUsed := uint64(params.BlobTxBlobGasPerBlob * 5)   // 5 blobs used
	expectedExcess := transaction.CalcExcessBlobGas(parentExcess, parentUsed)

	parent := &block.Header{
		BlobGasUsed:   u64ptr(parentUsed),
		ExcessBlobGas: u64ptr(parentExcess),
	}
	child := &block.Header{
		BlobGasUsed:   u64ptr(params.BlobTxBlobGasPerBlob),
		ExcessBlobGas: u64ptr(expectedExcess),
	}

	err := VerifyEIP4844Header(parent, child, nil)
	if err != nil {
		t.Fatalf("accumulated excess should be valid: %v", err)
	}

	// Verify the excess is actually nonzero in this scenario.
	if expectedExcess == 0 {
		t.Fatal("expected nonzero excess for this test case")
	}
}

func TestVerifyEIP4844Header_OsakaReservePrice(t *testing.T) {
	cfg := &params.ChainConfig{
		PragueTime: big.NewInt(0),
		OsakaTime:  big.NewInt(0),
	}
	parent := &block.Header{
		Time:          1,
		BaseFee:       uint256.NewInt(108),
		BlobGasUsed:   u64ptr(0),
		ExcessBlobGas: u64ptr(6 * params.BlobTxBlobGasPerBlob),
	}
	child := &block.Header{
		Time:          2,
		BlobGasUsed:   u64ptr(params.BlobTxBlobGasPerBlob),
		ExcessBlobGas: u64ptr(6 * params.BlobTxBlobGasPerBlob),
	}

	err := VerifyEIP4844Header(parent, child, cfg)
	if err != nil {
		t.Fatalf("osaka reserve-price header should pass: %v", err)
	}
}

func TestCalcBlobGasUsed_NoBlobs(t *testing.T) {
	txs := []*transaction.Transaction{}
	gas := CalcBlobGasUsed(txs)
	if gas != 0 {
		t.Fatalf("expected 0 for no transactions, got %d", gas)
	}
}

func TestCalcBlobGasUsed_NilSlice(t *testing.T) {
	gas := CalcBlobGasUsed(nil)
	if gas != 0 {
		t.Fatalf("expected 0 for nil transactions, got %d", gas)
	}
}

func TestCalcBlobGasUsed_NonBlobTxs(t *testing.T) {
	// Regular (non-blob) transactions should return 0 blob gas.
	// Create a basic legacy transaction.
	tx := transaction.NewTransaction(0, types.Address{1}, nil, nil, 21000, nil, nil)
	txs := []*transaction.Transaction{tx}
	gas := CalcBlobGasUsed(txs)
	if gas != 0 {
		t.Fatalf("expected 0 for non-blob transactions, got %d", gas)
	}
}

func u64ptr(v uint64) *uint64 {
	return &v
}
