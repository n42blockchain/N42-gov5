// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func apiTNewDebugAPI(t *testing.T) *DebugAPI {
	t.Helper()
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
	return NewDebugAPI(a)
}

func TestDebugAPI_GetBlockRlp(t *testing.T) {
	d := apiTNewDebugAPI(t)
	enc, err := d.GetBlockRlp(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetBlockRlp(0) error = %v", err)
	}
	if len(enc) == 0 {
		t.Fatal("GetBlockRlp(0) returned empty bytes")
	}

	if _, err := d.GetBlockRlp(context.Background(), 99); err == nil {
		t.Fatal("GetBlockRlp(99) error = nil, want not found error")
	}
}

func TestDebugAPI_GetHeaderRlp(t *testing.T) {
	d := apiTNewDebugAPI(t)
	enc, err := d.GetHeaderRlp(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetHeaderRlp(0) error = %v", err)
	}
	if len(enc) == 0 {
		t.Fatal("GetHeaderRlp(0) returned empty bytes")
	}

	if _, err := d.GetHeaderRlp(context.Background(), 99); err == nil {
		t.Fatal("GetHeaderRlp(99) error = nil, want not found error")
	}
}

func TestDebugAPI_PrintBlock(t *testing.T) {
	d := apiTNewDebugAPI(t)
	s, err := d.PrintBlock(context.Background(), 0)
	if err != nil {
		t.Fatalf("PrintBlock(0) error = %v", err)
	}
	if !strings.Contains(s, "{") {
		t.Fatalf("PrintBlock(0) = %q, want JSON-ish output", s)
	}

	if _, err := d.PrintBlock(context.Background(), 99); err == nil {
		t.Fatal("PrintBlock(99) error = nil, want not found error")
	}
}

func TestDebugAPI_GetBadBlocks(t *testing.T) {
	d := apiTNewDebugAPI(t)
	bad, err := d.GetBadBlocks(context.Background())
	if err != nil {
		t.Fatalf("GetBadBlocks() error = %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("GetBadBlocks() = %v, want empty", bad)
	}
}

func TestDebugAPI_AccountRange(t *testing.T) {
	d := apiTNewDebugAPI(t)
	res, err := d.AccountRange(context.Background(), jsonrpc.BlockNumberOrHashWithNumber(0), nil, 10, false, false, false)
	if err != nil {
		t.Fatalf("AccountRange() error = %v", err)
	}
	if res == nil || res.Accounts == nil {
		t.Fatal("AccountRange() returned nil result/accounts map")
	}
}

func TestDebugAPI_TrieFlushInterval(t *testing.T) {
	d := apiTNewDebugAPI(t)
	if err := d.SetTrieFlushInterval("5m"); err != nil {
		t.Fatalf("SetTrieFlushInterval() error = %v", err)
	}
	if got := d.GetTrieFlushInterval(); got != "1h0m0s" {
		t.Fatalf("GetTrieFlushInterval() = %q, want default", got)
	}
}
