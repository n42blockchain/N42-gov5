// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/blockhashindex"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/params"
)

func TestAPI_SetHistoricalStateSource(t *testing.T) {
	a := &API{}
	if a.historicalState != nil {
		t.Fatal("expected nil historicalState before Set")
	}
	a.SetHistoricalStateSource(nil)
	if a.historicalState != nil {
		t.Fatal("SetHistoricalStateSource(nil) should leave nil")
	}
}

func TestAPI_SetBlockHashIndex(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}

	// nil svc is a no-op.
	a.SetBlockHashIndex(nil)
	if a.blockHashIndex != nil {
		t.Fatal("SetBlockHashIndex(nil) should leave blockHashIndex nil")
	}

	svc, err := blockhashindex.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	a.SetBlockHashIndex(svc)
	if a.blockHashIndex == nil {
		t.Fatal("SetBlockHashIndex(svc) did not install the service")
	}

	// coldBlockByHash exercises the installed verifier via Lookup (empty
	// index, so this is just the no-hit path).
	if blk := a.coldBlockByHash(types.HexToHash("0xdead")); blk != nil {
		t.Fatalf("coldBlockByHash(unknown) = %v, want nil", blk)
	}
}

func TestAPI_SetTxIndexService(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}

	a.SetTxIndexService(nil)

	svc, err := txlookup.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	a.SetTxIndexService(svc)
}

func TestAPI_CurrentHeadNumber(t *testing.T) {
	var nilAPI *API
	if got := nilAPI.currentHeadNumber(); got != 0 {
		t.Fatalf("currentHeadNumber(nil API) = %d, want 0", got)
	}

	a := &API{}
	if got := a.currentHeadNumber(); got != 0 {
		t.Fatalf("currentHeadNumber(nil bc) = %d, want 0", got)
	}

	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a2 := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
	if got := a2.currentHeadNumber(); got != 0 {
		t.Fatalf("currentHeadNumber(genesis) = %d, want 0", got)
	}
}

func TestDebugAPI_GetAccount_NoOp(t *testing.T) {
	d := apiTNewDebugAPI(t)
	// GetAccount is a no-op placeholder; just exercise it for coverage.
	d.GetAccount(context.Background(), types.Address{})
}

func TestAPI_GetEVM_NoEngine(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}

	msg := transaction.NewMessage(types.Address{}, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(1), uint256.NewInt(1), uint256.NewInt(1), nil, nil, nil, nil, false, false)
	header, _ := genesis.Header().(*block.Header)
	_, _, err := a.GetEVM(context.Background(), &msg, nil, header, nil)
	if err == nil {
		t.Fatal("GetEVM() error = nil, want consensus engine not available")
	}
}

func TestAPI_GetTransaction_NotFound(t *testing.T) {
	bca := setupStorageTestAPI(t)
	tx, _, _, _, err := bca.api.GetTransaction(context.Background(), types.HexToHash("0xdead"))
	if err != nil {
		t.Fatalf("GetTransaction() error = %v", err)
	}
	if tx != nil {
		t.Fatalf("GetTransaction(unknown) = %v, want nil", tx)
	}
}
