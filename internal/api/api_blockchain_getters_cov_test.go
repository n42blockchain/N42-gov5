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
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func TestBlockChainAPI_ChainId(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(7)}
	bca := NewBlockChainAPI(&API{chainConfig: cfg})
	id := bca.ChainId()
	if id == nil || (*big.Int)(id).Cmp(big.NewInt(7)) != 0 {
		t.Fatalf("ChainId() = %v, want 7", id)
	}

	if (*BlockChainAPI)(nil).ChainId() != nil {
		t.Fatal("ChainId() on nil receiver, want nil")
	}

	noCfg := NewBlockChainAPI(&API{})
	if got := noCfg.ChainId(); got != nil {
		t.Fatalf("ChainId() with nil chainConfig = %v, want nil", got)
	}
}

func TestBlockChainAPI_GetBalanceGetCodeGetStorageAt(t *testing.T) {
	bca := setupStorageTestAPI(t)
	contract := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)

	balance, err := bca.GetBalance(context.Background(), avmcommon.Address(contract), latest)
	if err != nil {
		t.Fatalf("GetBalance() error = %v", err)
	}
	if balance == nil {
		t.Fatal("GetBalance() = nil")
	}

	code, err := bca.GetCode(context.Background(), avmcommon.Address(contract), latest)
	if err != nil {
		t.Fatalf("GetCode() error = %v", err)
	}
	// Contract has no deployed code in the fixture, just storage; empty is fine.
	_ = code

	// GetStorageAt must not error for a resolvable state, regardless of
	// whether the given slot happens to be populated.
	if _, err := bca.GetStorageAt(context.Background(), contract,
		"0x0000000000000000000000000000000000000000000000000000000000000001", latest); err != nil {
		t.Fatalf("GetStorageAt() error = %v", err)
	}
}

func TestBlockChainAPI_BlockNumberAndEarliestBlock(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	block1 := makeTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, 2)
	stub := newPreciseChainStub(cfg, nil, genesis, block1)
	api := &API{bc: stub, chainConfig: cfg}
	bca := NewBlockChainAPI(api)

	if got := bca.BlockNumber(); got != 1 {
		t.Fatalf("BlockNumber() = %d, want 1", got)
	}
	if got := bca.EarliestBlock(); got != 0 {
		t.Fatalf("EarliestBlock() = %d, want 0", got)
	}
}

func TestBlockChainAPI_GetUncleHelpers(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	stub := newPreciseChainStub(cfg, nil, genesis)
	api := &API{bc: stub, chainConfig: cfg}
	bca := NewBlockChainAPI(api)

	genesisHash := ethCompatibleBlockHash(genesis, cfg)
	count := bca.GetUncleCountByBlockHash(context.Background(), avmcommon.Hash(avmtypes.FromastHash(genesisHash)))
	if count == nil || *count != 0 {
		t.Fatalf("GetUncleCountByBlockHash(known) = %v, want 0", count)
	}

	missing := bca.GetUncleCountByBlockHash(context.Background(), avmcommon.Hash(avmtypes.FromastHash(types.HexToHash("0xdead"))))
	if missing != nil {
		t.Fatalf("GetUncleCountByBlockHash(unknown) = %v, want nil", missing)
	}

	uncle, err := bca.GetUncleByBlockHashAndIndex(context.Background(), avmcommon.Hash{}, 0)
	if err != nil {
		t.Fatalf("GetUncleByBlockHashAndIndex() error = %v", err)
	}
	if uncle != nil {
		t.Fatalf("GetUncleByBlockHashAndIndex() = %v, want nil (no uncles)", uncle)
	}
}
