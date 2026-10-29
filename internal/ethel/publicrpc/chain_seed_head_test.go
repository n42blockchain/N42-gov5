// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package publicrpc

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

// A snapshot-seeded datadir carries the head marker and the head's state, but
// the head block itself lives in headerc/bodyc, not in MDBX. Until the first
// peer block is imported CurrentBlock has nothing to return, and eth_blockNumber
// used to answer 0 for a node sitting at the seed head.
func TestBlockNumber_SeedHeadBeforeFirstImport(t *testing.T) {
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := ethel.WriteHeadMarker(tx, 26245274); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	chain := newEthelChain(db, nil, &params.ChainConfig{}, nil)
	if blk := chain.CurrentBlock(); blk != nil {
		t.Fatalf("fixture: CurrentBlock() = block %v, want nil (no block in MDBX)", blk.Number64())
	}
	core := api.NewAPI(chain, db, nil, nil, nil, &params.ChainConfig{})
	if got := api.NewBlockChainAPI(core).BlockNumber(); got != 26245274 {
		t.Fatalf("BlockNumber() = %d, want 26245274 (the seed head)", got)
	}
}
