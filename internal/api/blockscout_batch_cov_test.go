// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func TestBlockChainAPI_GetBlockscoutCompatibility(t *testing.T) {
	bca := NewBlockChainAPI(&API{})
	info := bca.GetBlockscoutCompatibility()
	if info == nil || !info.Compatible {
		t.Fatalf("GetBlockscoutCompatibility() = %+v, want Compatible=true", info)
	}
	if info.Features == nil || !info.Features.EIP1559 {
		t.Fatal("GetBlockscoutCompatibility().Features.EIP1559 = false, want true")
	}
	if info.Features.UncleBlocks {
		t.Fatal("GetBlockscoutCompatibility().Features.UncleBlocks = true, want false (PoA/PoS)")
	}
}

func TestBlockChainAPI_BatchGetBalanceAndCode(t *testing.T) {
	bca := setupStorageTestAPI(t)
	contract := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)

	balances, err := bca.BatchGetBalance(context.Background(), []types.Address{contract, {}}, latest)
	if err != nil {
		t.Fatalf("BatchGetBalance() error = %v", err)
	}
	if len(balances) != 2 {
		t.Fatalf("BatchGetBalance() returned %d entries, want 2", len(balances))
	}

	codes, err := bca.BatchGetCode(context.Background(), []types.Address{contract, {}}, latest)
	if err != nil {
		t.Fatalf("BatchGetCode() error = %v", err)
	}
	if len(codes) != 2 {
		t.Fatalf("BatchGetCode() returned %d entries, want 2", len(codes))
	}
}

func TestBlockChainAPI_BatchGetBalanceAndCodeTooMany(t *testing.T) {
	bca := setupStorageTestAPI(t)
	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	addrs := make([]types.Address, maxBatchAddresses+1)

	if _, err := bca.BatchGetBalance(context.Background(), addrs, latest); err == nil {
		t.Fatal("BatchGetBalance(too many addresses) error = nil, want error")
	}
	if _, err := bca.BatchGetCode(context.Background(), addrs, latest); err == nil {
		t.Fatal("BatchGetCode(too many addresses) error = nil, want error")
	}
}

// NOTE: BlobBaseFee's "no current block" branch shares the typed-nil IBlock
// hazard documented next to API.CurrentBlock(): a chain stub constructed
// with zero blocks leaves `current` a nil *block.Block wrapped in the
// block.IBlock interface, so `currentBlock == nil` is false and the
// following currentBlock.Header() call panics instead of returning the
// "no current block" error. Exercised instead via the normal path below
// with a real genesis block present.
func TestBlockChainAPI_BlobBaseFeeWithGenesis(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
	bca := NewBlockChainAPI(api)

	fee, err := bca.BlobBaseFee(context.Background())
	if err != nil {
		t.Fatalf("BlobBaseFee() error = %v", err)
	}
	if fee == nil {
		t.Fatal("BlobBaseFee() = nil, want a value (no blob excess on genesis defaults to the floor)")
	}
}
