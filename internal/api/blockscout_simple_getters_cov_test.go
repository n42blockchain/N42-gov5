// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

type fakeMinerAdmin struct {
	mining bool
}

func (f *fakeMinerAdmin) Mining() bool               { return f.mining }
func (f *fakeMinerAdmin) SetCoinbase(types.Address) {}

func TestBlockChainAPI_SyncingNotSyncing(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
	bca := NewBlockChainAPI(api)

	res, err := bca.Syncing()
	if err != nil {
		t.Fatalf("Syncing() error = %v", err)
	}
	synced, ok := res.(bool)
	if !ok || synced != false {
		t.Fatalf("Syncing() = %v (%T), want false", res, res)
	}
}

func TestBlockChainAPI_SyncingBehindPeer(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{
		bc:          newPreciseChainStub(cfg, nil, genesis),
		chainConfig: cfg,
		p2p:         &fakePeerInfoAdmin{highest: 100},
	}
	bca := NewBlockChainAPI(api)

	res, err := bca.Syncing()
	if err != nil {
		t.Fatalf("Syncing() error = %v", err)
	}
	progress, ok := res.(*SyncProgress)
	if !ok {
		t.Fatalf("Syncing() = %v (%T), want *SyncProgress", res, res)
	}
	if progress.HighestBlock != 100 {
		t.Fatalf("Syncing().HighestBlock = %d, want 100", progress.HighestBlock)
	}
}

func TestBlockChainAPI_Coinbase(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
	bca := NewBlockChainAPI(api)

	addr, err := bca.Coinbase()
	if err != nil {
		t.Fatalf("Coinbase() error = %v", err)
	}
	_ = addr // zero coinbase on the synthetic genesis; just confirm no panic/error.
}

func TestBlockChainAPI_MiningAndHashrate(t *testing.T) {
	api := &API{}
	bca := NewBlockChainAPI(api)
	if bca.Mining() != false {
		t.Fatal("Mining() with nil miner = true, want false")
	}
	if bca.Hashrate() != 0 {
		t.Fatal("Hashrate() != 0, want 0 (PoA/PoS has no hashrate)")
	}

	api.miner = &fakeMinerAdmin{mining: true}
	if bca.Mining() != true {
		t.Fatal("Mining() with mining miner = false, want true")
	}
}

func TestBlockChainAPI_GetBlockTransactionCountByNumber(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
	bca := NewBlockChainAPI(api)

	count, err := bca.GetBlockTransactionCountByNumber(context.Background(), jsonrpc.BlockNumber(0))
	if err != nil {
		t.Fatalf("GetBlockTransactionCountByNumber(0) error = %v", err)
	}
	if count == nil || *count != 0 {
		t.Fatalf("GetBlockTransactionCountByNumber(0) = %v, want 0", count)
	}

	missing, err := bca.GetBlockTransactionCountByNumber(context.Background(), jsonrpc.BlockNumber(99))
	if err != nil {
		t.Fatalf("GetBlockTransactionCountByNumber(99) error = %v", err)
	}
	if missing != nil {
		t.Fatalf("GetBlockTransactionCountByNumber(99) = %v, want nil", missing)
	}
}

func TestBlockChainAPI_GetUncleCountAndByIndexByNumber(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
	bca := NewBlockChainAPI(api)

	count, err := bca.GetUncleCountByBlockNumber(context.Background(), jsonrpc.BlockNumber(0))
	if err != nil {
		t.Fatalf("GetUncleCountByBlockNumber(0) error = %v", err)
	}
	if count == nil || *count != 0 {
		t.Fatalf("GetUncleCountByBlockNumber(0) = %v, want 0", count)
	}

	missing, err := bca.GetUncleCountByBlockNumber(context.Background(), jsonrpc.BlockNumber(99))
	if err != nil {
		t.Fatalf("GetUncleCountByBlockNumber(99) error = %v", err)
	}
	if missing != nil {
		t.Fatalf("GetUncleCountByBlockNumber(99) = %v, want nil", missing)
	}

	uncle, err := bca.GetUncleByBlockNumberAndIndex(context.Background(), jsonrpc.BlockNumber(0), 0)
	if err != nil {
		t.Fatalf("GetUncleByBlockNumberAndIndex() error = %v", err)
	}
	if uncle != nil {
		t.Fatalf("GetUncleByBlockNumberAndIndex() = %v, want nil (no uncles)", uncle)
	}
}

func TestBlockChainAPI_AccountsNilAndEmptyManager(t *testing.T) {
	bca := NewBlockChainAPI(&API{})
	if got := bca.Accounts(); len(got) != 0 {
		t.Fatalf("Accounts() with nil manager = %v, want empty", got)
	}

	bca2 := NewBlockChainAPI(&API{accountManager: &accounts.Manager{}})
	if got := bca2.Accounts(); len(got) != 0 {
		t.Fatalf("Accounts() with empty manager = %v, want empty", got)
	}
}
