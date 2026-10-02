// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common/types"
	rpc "github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func newAccessorTestAPI(t *testing.T) (*API, *preciseChainStub) {
	t.Helper()
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	block1 := makeTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, 2)
	stub := newPreciseChainStub(cfg, nil, genesis, block1)
	api := &API{
		bc:             stub,
		chainConfig:    cfg,
		accountManager: &accounts.Manager{},
	}
	return api, stub
}

func TestAPI_ChainConfigAndCurrentBlock(t *testing.T) {
	api, _ := newAccessorTestAPI(t)
	if api.ChainConfig() == nil {
		t.Fatal("ChainConfig() = nil")
	}
	hdr := api.CurrentBlock()
	if hdr == nil {
		t.Fatal("CurrentBlock() = nil")
	}
	// CurrentHeader is a direct alias.
	if api.CurrentHeader() == nil {
		t.Fatal("CurrentHeader() = nil")
	}
}

// NOTE: API.CurrentBlock() does not guard against a typed-nil *block.Block
// wrapped in the block.IBlock interface: newPreciseChainStub(cfg, nil) (no
// blocks) leaves `current` as a nil *block.Block, so `blk == nil` at
// api_backend.go:59 is false (non-nil interface, nil concrete value) and the
// subsequent blk.Header() call panics. This is a real defect in the
// production code (common.IBlockChain.CurrentBlock() implementations that
// can return a typed-nil block will crash every RPC accessor built on top of
// CurrentBlock/CurrentHeader), left unfixed per task scope. Covered
// indirectly above via a stub that always returns a concrete *block.Block.

func TestAPI_HeaderByNumberLatestAndMissing(t *testing.T) {
	api, _ := newAccessorTestAPI(t)

	hdr, err := api.HeaderByNumber(context.Background(), rpc.BlockNumber(1))
	if err != nil {
		t.Fatalf("HeaderByNumber(1) error = %v", err)
	}
	if hdr == nil {
		t.Fatal("HeaderByNumber(1) = nil")
	}

	missing, err := api.HeaderByNumber(context.Background(), rpc.BlockNumber(99))
	if err != nil {
		t.Fatalf("HeaderByNumber(99) error = %v", err)
	}
	if missing != nil {
		t.Fatalf("HeaderByNumber(99) = %v, want nil", missing)
	}
}

func TestAPI_HeaderByHashAndByNumberOrHash(t *testing.T) {
	api, stub := newAccessorTestAPI(t)
	cfg := api.chainConfig
	genesisHash := ethCompatibleBlockHash(stub.blocksByNumber[0], cfg)

	hdr, err := api.HeaderByHash(context.Background(), genesisHash)
	if err != nil {
		t.Fatalf("HeaderByHash() error = %v", err)
	}
	if hdr == nil {
		t.Fatal("HeaderByHash() = nil for known genesis hash")
	}

	missingHdr, err := api.HeaderByHash(context.Background(), types.HexToHash("0xdead"))
	if err != nil {
		t.Fatalf("HeaderByHash(unknown) error = %v", err)
	}
	if missingHdr != nil {
		t.Fatal("HeaderByHash(unknown) != nil, want nil")
	}

	bnh := rpc.BlockNumberOrHashWithHash(genesisHash, false)
	hdr2, err := api.HeaderByNumberOrHash(context.Background(), bnh)
	if err != nil {
		t.Fatalf("HeaderByNumberOrHash(hash) error = %v", err)
	}
	if hdr2 == nil {
		t.Fatal("HeaderByNumberOrHash(hash) = nil")
	}

	bnhByNum := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(1))
	hdr3, err := api.HeaderByNumberOrHash(context.Background(), bnhByNum)
	if err != nil {
		t.Fatalf("HeaderByNumberOrHash(number) error = %v", err)
	}
	if hdr3 == nil {
		t.Fatal("HeaderByNumberOrHash(number) = nil")
	}
}

func TestAPI_HeaderByNumberOrHashUnknownHash(t *testing.T) {
	api, _ := newAccessorTestAPI(t)
	bnh := rpc.BlockNumberOrHashWithHash(types.HexToHash("0xdead"), false)
	_, err := api.HeaderByNumberOrHash(context.Background(), bnh)
	if err == nil {
		t.Fatal("HeaderByNumberOrHash(unknown hash) error = nil, want error")
	}
}

func TestAPI_BlockByNumberAndByHash(t *testing.T) {
	api, stub := newAccessorTestAPI(t)
	cfg := api.chainConfig

	blk, err := api.BlockByNumber(context.Background(), rpc.BlockNumber(1))
	if err != nil {
		t.Fatalf("BlockByNumber(1) error = %v", err)
	}
	if blk == nil {
		t.Fatal("BlockByNumber(1) = nil")
	}

	missing, err := api.BlockByNumber(context.Background(), rpc.BlockNumber(99))
	if err != nil {
		t.Fatalf("BlockByNumber(99) error = %v", err)
	}
	if missing != nil {
		t.Fatal("BlockByNumber(99) != nil, want nil")
	}

	genesisHash := ethCompatibleBlockHash(stub.blocksByNumber[0], cfg)
	byHash, err := api.BlockByHash(context.Background(), genesisHash)
	if err != nil {
		t.Fatalf("BlockByHash() error = %v", err)
	}
	if byHash == nil {
		t.Fatal("BlockByHash() = nil for known genesis hash")
	}

	missingByHash, err := api.BlockByHash(context.Background(), types.HexToHash("0xdead"))
	if err != nil {
		t.Fatalf("BlockByHash(unknown) error = %v", err)
	}
	if missingByHash != nil {
		t.Fatal("BlockByHash(unknown) != nil, want nil")
	}
}

func TestAPI_BlockByNumberOrHash(t *testing.T) {
	api, stub := newAccessorTestAPI(t)
	cfg := api.chainConfig
	genesisHash := ethCompatibleBlockHash(stub.blocksByNumber[0], cfg)

	byNum, err := api.BlockByNumberOrHash(context.Background(), rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(1)))
	if err != nil {
		t.Fatalf("BlockByNumberOrHash(number) error = %v", err)
	}
	if byNum == nil {
		t.Fatal("BlockByNumberOrHash(number) = nil")
	}

	byHash, err := api.BlockByNumberOrHash(context.Background(), rpc.BlockNumberOrHashWithHash(genesisHash, false))
	if err != nil {
		t.Fatalf("BlockByNumberOrHash(hash) error = %v", err)
	}
	if byHash == nil {
		t.Fatal("BlockByNumberOrHash(hash) = nil")
	}

	_, err = api.BlockByNumberOrHash(context.Background(), rpc.BlockNumberOrHashWithHash(types.HexToHash("0xdead"), false))
	if err == nil {
		t.Fatal("BlockByNumberOrHash(unknown hash) error = nil, want error")
	}
}

func TestAPI_GetReceiptsAndGetTd(t *testing.T) {
	api, stub := newAccessorTestAPI(t)
	cfg := api.chainConfig
	genesisHash := ethCompatibleBlockHash(stub.blocksByNumber[0], cfg)

	receipts, err := api.GetReceipts(context.Background(), genesisHash)
	if err != nil {
		t.Fatalf("GetReceipts() error = %v", err)
	}
	if receipts == nil {
		// preciseChainStub returns an empty (possibly nil) slice; just check no panic.
		t.Log("GetReceipts() returned nil receipts for genesis (expected, none registered)")
	}

	// GetTd on an unknown hash returns nil without error.
	if td := api.GetTd(context.Background(), types.HexToHash("0xdead")); td != nil {
		t.Fatalf("GetTd(unknown) = %v, want nil", td)
	}
}

func TestAPI_ChainDbAccountManagerCurrentHeader(t *testing.T) {
	api, _ := newAccessorTestAPI(t)
	if api.ChainDb() != nil {
		t.Fatal("ChainDb() expected nil (no db configured in this test)")
	}
	if api.AccountManager() == nil {
		t.Fatal("AccountManager() = nil")
	}
}
