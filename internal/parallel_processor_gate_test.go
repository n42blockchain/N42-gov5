// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers BuildParallel's fee-recipient gate (ErrParallelNotApplicable when a
// candidate transaction touches the block's own fee recipient, since the
// deferred-credit optimization cannot apply) and BlockChain.BuildParallel's
// not-a-StateProcessor guard.

package internal

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestBuildParallelRejectsTxTouchingFeeRecipient(t *testing.T) {
	coinbase := types.HexToAddress("0xc0ffee")
	header := &block.Header{Coinbase: coinbase}
	p := &StateProcessor{config: &params.ChainConfig{}}

	to := types.HexToAddress("0x2")
	txToCoinbase := transaction.NewTransaction(0, coinbase, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)

	_, err := p.BuildParallel(header, []*transaction.Transaction{txToCoinbase}, nil, nil)
	if err != ErrParallelNotApplicable {
		t.Fatalf("BuildParallel(tx from coinbase) = %v, want ErrParallelNotApplicable", err)
	}
}

func TestBlockChainBuildParallelRejectsNonStateProcessor(t *testing.T) {
	bc := &BlockChain{} // process is nil, not a *StateProcessor
	_, _, _, _, err := bc.BuildParallel(&block.Header{}, nil, nil, nil)
	if err != ErrParallelNotApplicable {
		t.Fatalf("BuildParallel() with no process = %v, want ErrParallelNotApplicable", err)
	}
}
