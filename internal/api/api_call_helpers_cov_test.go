// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/types"
	internalcore "github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func TestN42API_GasPriceAndTipNilOracle(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{
		bc:          newPreciseChainStub(cfg, nil, genesis),
		chainConfig: cfg,
	}
	n42 := NewN42API(api)

	price, err := n42.GasPrice(context.Background())
	if err != nil {
		t.Fatalf("GasPrice() error = %v", err)
	}
	if price == nil || (*big.Int)(price).Sign() <= 0 {
		t.Fatalf("GasPrice() = %v, want positive default", price)
	}

	tip, err := n42.MaxPriorityFeePerGas(context.Background())
	if err != nil {
		t.Fatalf("MaxPriorityFeePerGas() error = %v", err)
	}
	if tip == nil || (*big.Int)(tip).Sign() <= 0 {
		t.Fatalf("MaxPriorityFeePerGas() = %v, want positive default", tip)
	}
}

func TestRevertError_CodeAndData(t *testing.T) {
	result := &internalcore.ExecutionResult{
		Err:        vm2.ErrExecutionReverted,
		ReturnData: []byte{},
	}
	revErr := newRevertError(result)
	if revErr.ErrorCode() != 3 {
		t.Fatalf("ErrorCode() = %d, want 3", revErr.ErrorCode())
	}
	if revErr.ErrorData() == nil {
		t.Fatal("ErrorData() = nil, want hex-encoded reason")
	}
	if revErr.Error() == "" {
		t.Fatal("Error() returned empty string")
	}
}

func TestCheckTxFee(t *testing.T) {
	// Zero gas price is always accepted.
	if err := checkTxFee(uint256.Int{}, 0); err != nil {
		t.Fatalf("checkTxFee(zero) error = %v, want nil", err)
	}

	// A gas price below the hard ceiling is accepted.
	low := *uint256.NewInt(1_000_000_000) // 1 Gwei
	if err := checkTxFee(low, 0); err != nil {
		t.Fatalf("checkTxFee(low) error = %v, want nil", err)
	}

	// A gas price above the hard ceiling (1000 Gwei) is rejected.
	high := *uint256.NewInt(2_000_000_000_000) // 2000 Gwei
	if err := checkTxFee(high, 0); err == nil {
		t.Fatal("checkTxFee(high) error = nil, want error")
	}
}

func TestToHexSlice(t *testing.T) {
	in := [][]byte{{0x01, 0x02}, {}, {0xff}}
	out := toHexSlice(in)
	if len(out) != 3 {
		t.Fatalf("toHexSlice() returned %d entries, want 3", len(out))
	}
	if out[0] != "0x0102" {
		t.Fatalf("toHexSlice()[0] = %q, want %q", out[0], "0x0102")
	}
	if out[1] != "0x" {
		t.Fatalf("toHexSlice()[1] = %q, want %q", out[1], "0x")
	}
}

func TestTransactionAPI_GetTransactionCountPending(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{
		bc:          newPreciseChainStub(cfg, nil, genesis),
		chainConfig: cfg,
		txspool:     &fakeTxsPool{},
	}
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.PendingBlockNumber)
	count, err := txAPI.GetTransactionCount(context.Background(), avmcommon.Address{}, bnh)
	if err != nil {
		t.Fatalf("GetTransactionCount(pending) error = %v", err)
	}
	if count == nil || *count != 0 {
		t.Fatalf("GetTransactionCount(pending) = %v, want 0", count)
	}
}
