// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func newTxGetterTestAPI(t *testing.T) (*API, *preciseChainStub) {
	t.Helper()
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	db := memdb.NewTestDB(t)
	stub := newPreciseChainStub(cfg, db, genesis)
	api := &API{
		db:          db,
		bc:          stub,
		chainConfig: cfg,
		txspool:     &fakeTxsPool{},
	}
	return api, stub
}

func TestTransactionAPI_GetBlockTransactionCountByHash(t *testing.T) {
	api, stub := newTxGetterTestAPI(t)
	cfg := api.chainConfig
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	genesisHash := ethCompatibleBlockHash(stub.blocksByNumber[0], cfg)
	count := txAPI.GetBlockTransactionCountByHash(context.Background(), avmcommon.Hash(avmtypes.FromastHash(genesisHash)))
	if count == nil || *count != 0 {
		t.Fatalf("GetBlockTransactionCountByHash(known, empty block) = %v, want 0", count)
	}

	missing := txAPI.GetBlockTransactionCountByHash(context.Background(), avmcommon.Hash(avmtypes.FromastHash(types.HexToHash("0xdead"))))
	if missing != nil {
		t.Fatalf("GetBlockTransactionCountByHash(unknown) = %v, want nil", missing)
	}
}

func TestTransactionAPI_GetTransactionByHashNotFound(t *testing.T) {
	api, _ := newTxGetterTestAPI(t)
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	rpcTx, err := txAPI.GetTransactionByHash(context.Background(), avmcommon.Hash(avmtypes.FromastHash(types.HexToHash("0xdead"))))
	if err != nil {
		t.Fatalf("GetTransactionByHash() error = %v", err)
	}
	if rpcTx != nil {
		t.Fatalf("GetTransactionByHash(unknown) = %v, want nil", rpcTx)
	}
}

func TestTransactionAPI_GetTransactionByBlockHashAndIndexMissing(t *testing.T) {
	api, stub := newTxGetterTestAPI(t)
	cfg := api.chainConfig
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	genesisHash := ethCompatibleBlockHash(stub.blocksByNumber[0], cfg)
	// Genesis block has no transactions, so index 0 must be nil.
	rpcTx := txAPI.GetTransactionByBlockHashAndIndex(context.Background(), avmcommon.Hash(avmtypes.FromastHash(genesisHash)), 0)
	if rpcTx != nil {
		t.Fatalf("GetTransactionByBlockHashAndIndex(empty block) = %v, want nil", rpcTx)
	}

	rpcTxUnknown := txAPI.GetTransactionByBlockHashAndIndex(context.Background(), avmcommon.Hash(avmtypes.FromastHash(types.HexToHash("0xdead"))), 0)
	if rpcTxUnknown != nil {
		t.Fatalf("GetTransactionByBlockHashAndIndex(unknown block) = %v, want nil", rpcTxUnknown)
	}
}

func TestBlockChainAPI_GetBlockByNumberAndHash(t *testing.T) {
	api, stub := newTxGetterTestAPI(t)
	cfg := api.chainConfig
	bca := NewBlockChainAPI(api)

	resp, err := bca.GetBlockByNumber(context.Background(), jsonrpc.BlockNumber(0), false)
	if err != nil {
		t.Fatalf("GetBlockByNumber(0) error = %v", err)
	}
	if resp == nil {
		t.Fatal("GetBlockByNumber(0) = nil")
	}

	missing, err := bca.GetBlockByNumber(context.Background(), jsonrpc.BlockNumber(99), false)
	if err != nil {
		t.Fatalf("GetBlockByNumber(99) error = %v", err)
	}
	if missing != nil {
		t.Fatalf("GetBlockByNumber(99) = %v, want nil", missing)
	}

	genesisHash := ethCompatibleBlockHash(stub.blocksByNumber[0], cfg)
	byHash, err := bca.GetBlockByHash(context.Background(), avmcommon.Hash(avmtypes.FromastHash(genesisHash)), false)
	if err != nil {
		t.Fatalf("GetBlockByHash(known) error = %v", err)
	}
	if byHash == nil {
		t.Fatal("GetBlockByHash(known) = nil")
	}

	missingByHash, err := bca.GetBlockByHash(context.Background(), avmcommon.Hash(avmtypes.FromastHash(types.HexToHash("0xdead"))), false)
	if err != nil {
		t.Fatalf("GetBlockByHash(unknown) error = %v", err)
	}
	if missingByHash != nil {
		t.Fatalf("GetBlockByHash(unknown) = %v, want nil", missingByHash)
	}
}
