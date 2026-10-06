// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers gateFrom's zero-means-genesis convention, nativeQMDBChain's
// QMDB-and-not-Ethereum-encoding predicate, and executedRootIn's success/
// not-found paths against a genesis header.

package internal

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/params"
)

func TestGateFromZeroAndNegativeMeanGenesis(t *testing.T) {
	if got := gateFrom(big.NewInt(0)); got != 1 {
		t.Fatalf("gateFrom(0) = %d, want 1 (from genesis)", got)
	}
	if got := gateFrom(big.NewInt(-5)); got != 1 {
		t.Fatalf("gateFrom(-5) = %d, want 1 (from genesis)", got)
	}
	if got := gateFrom(big.NewInt(100)); got != 100 {
		t.Fatalf("gateFrom(100) = %d, want 100", got)
	}
}

func TestNativeQMDBChain(t *testing.T) {
	if nativeQMDBChain(nil) {
		t.Fatalf("nativeQMDBChain(nil) = true, want false")
	}
	qmdb := &params.ChainConfig{StateScheme: string(params.StateCommitmentPresetQMDB)}
	if !nativeQMDBChain(qmdb) {
		t.Fatalf("nativeQMDBChain(qmdb, no ethereum receipts) = false, want true")
	}
	mpt := &params.ChainConfig{StateScheme: string(params.StateCommitmentPresetEthereumMPT)}
	if nativeQMDBChain(mpt) {
		t.Fatalf("nativeQMDBChain(mpt) = true, want false")
	}
}

func TestExecutedRootInReadsGenesisHeaderRoot(t *testing.T) {
	bc, genesis, _ := newReaderTestChain(t)
	bc.chainConfig = &params.ChainConfig{}

	h := genesis.Header().(*block.Header)
	var (
		gotRoot types.Hash
		gotOk   bool
	)
	if err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		gotRoot, gotOk = bc.executedRootIn(tx, h)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Before the deferred-execution fork (and at genesis), the root is the
	// header's own Root field, read straight through ExecutedResultOfHeader.
	if !gotOk || gotRoot != h.Root {
		t.Fatalf("executedRootIn(genesis header) = (%s, %v), want (%s, true)", gotRoot.Hex(), gotOk, h.Root.Hex())
	}
}
