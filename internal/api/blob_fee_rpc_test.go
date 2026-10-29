// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// eth_blobBaseFee must price blob gas with the blob schedule active at the
// head, as execution does. From Prague on the update fraction is no longer
// Cancun's 3,338,477, so the Cancun-only helper reports a wrong fee.
func TestBlobBaseFee_UsesScheduleAtHead(t *testing.T) {
	cfg := params.EthereumMainnetChainConfig
	const pragueTime = 1746612311
	const excess = 50_000_000

	genesis := makeTestBlock(types.Hash{}, 0, pragueTime)
	headHeader := makeTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, pragueTime+12).Header().(*block.Header)
	excessVal := uint64(excess)
	headHeader.ExcessBlobGas = &excessVal
	head := block.NewBlock(headHeader, nil).(*block.Block)

	api := &API{bc: newPreciseChainStub(cfg, nil, genesis, head), chainConfig: cfg}
	got, err := NewBlockChainAPI(api).BlobBaseFee(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := cfg.CalcBlobFee(excess, pragueTime+12)
	if cancun := transaction.CalcBlobFee(excess); cancun.Eq(want) {
		t.Fatalf("fixture: Prague and Cancun fees coincide (%s); pick another excess", want)
	}
	if got.ToInt().Cmp(want.ToBig()) != 0 {
		t.Fatalf("BlobBaseFee = %s, want %s (Prague schedule)", got.ToInt(), want)
	}
}
