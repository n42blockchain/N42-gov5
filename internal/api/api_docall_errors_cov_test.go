// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

// TestDoCall_OversizedCalldataRejected exercises the maxCallDataSize guard at
// the top of DoCall, which fails fast before touching chain/state at all.
func TestDoCall_OversizedCalldataRejected(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}

	big := make(hexutil.Bytes, maxCallDataSize+1)
	args := TransactionArgs{Data: &big}

	_, err := DoCall(context.Background(), api, args, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber), nil, 0, 0)
	if err == nil {
		t.Fatal("DoCall(oversized calldata) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "exceeds maximum allowed") {
		t.Fatalf("DoCall(oversized calldata) error = %v, want calldata-size message", err)
	}
}

// TestDoCall_BlockNotFound exercises the header-resolution failure path: a
// specific block number the chain doesn't know about.
func TestDoCall_BlockNotFound(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}

	_, err := DoCall(context.Background(), api, TransactionArgs{}, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(99)), nil, 0, 0)
	if err == nil {
		t.Fatal("DoCall(unknown block number) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "block not found") {
		t.Fatalf("DoCall(unknown block number) error = %v, want 'block not found'", err)
	}
}

// TestDoCall_UnknownHash exercises the hash-resolution error path.
func TestDoCall_UnknownHash(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}

	_, err := DoCall(context.Background(), api, TransactionArgs{}, jsonrpc.BlockNumberOrHashWithHash(types.HexToHash("0xdead"), false), nil, 0, 0)
	if err == nil {
		t.Fatal("DoCall(unknown hash) error = nil, want error")
	}
}

// TestBlockChainAPI_CallOversizedCalldata exercises the thin Call() wrapper
// over DoCall's argument-validation error path.
func TestBlockChainAPI_CallOversizedCalldata(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
	bca := NewBlockChainAPI(api)

	big := make(hexutil.Bytes, maxCallDataSize+1)
	_, err := bca.Call(context.Background(), TransactionArgs{Data: &big}, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber), nil)
	if err == nil {
		t.Fatal("Call(oversized calldata) error = nil, want error")
	}
}

// NOTE: BlockChainAPI.EstimateGas's nil-blockNrOrHash default (pending) hits
// the same typed-nil *block.Block hazard documented next to CurrentBlock():
// resolveForkchoiceTaggedBlock() on a chain with no current block returns a
// non-nil IBlock interface wrapping a nil *block.Block, and DoEstimateGas
// then dereferences it (block.GasLimit()) and panics instead of returning an
// error. Not exercised here as a passing test since it would only pass by
// asserting a panic; left to the production-code owners per task scope.
