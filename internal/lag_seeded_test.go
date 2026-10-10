// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// TestLagGateOpensAtOnceOnAReseededChain reproduces S91: the fleet was
// reseeded from a snapshot at head H (depth-1 history, no applied marker)
// and every leader's first view handed the gate the zero parent (no LockedQC
// block yet), which it waited on for ever. The zero parent is the canonical
// head, and the seeded state counts as executed up to it: the gate for H+1
// (reporting H-1, whose result only the header of H carries) and for H+2
// (reporting H, stored) is ready at once, and the header the builder stamps
// for H+1 is that result. An unknown non-zero parent still waits on itself,
// and once a block has been applied here the seeded rule is off.
func TestLagGateOpensAtOnceOnAReseededChain(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	db := memdb.NewTestDB(t)
	hc, _ := lru.New[types.Hash, *block.Header](64)
	bc := &BlockChain{ChainDB: db, ctx: context.Background(), headerCache: hc,
		chainConfig: &params.ChainConfig{DeferredExecutionTime: bigInt(1000)}}
	const H = 13652362
	var hs []*block.Header
	parent := types.Hash{0x01}
	for i, n := range []uint64{H - 2, H - 1, H} {
		h := &block.Header{Number: uint256.NewInt(n), Time: 2000 + uint64(i), ParentHash: parent,
			Root: types.Hash{byte(0x10 + i)}, ReceiptHash: types.Hash{byte(0x20 + i)}, GasUsed: uint64(100 + i),
			GasLimit: 1 << 30, Difficulty: uint256.NewInt(0)}
		hs = append(hs, h)
		parent = h.Hash()
	}
	headRes := rawdb.ExecutedResult{Root: types.Hash{0x99}, GasUsed: 7}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for _, h := range hs {
			rawdb.WriteHeader(tx, h)
			if err := rawdb.WriteCanonicalHash(tx, h.Hash(), h.Number.Uint64()); err != nil {
				return err
			}
		}
		// The snapshot keeps the head's own result, not H-1's.
		return rawdb.WriteExecutedResult(tx, hs[2].Hash(), headRes)
	}); err != nil {
		t.Fatal(err)
	}
	bc.currentBlock.Store(block.NewBlock(hs[2], nil).(*block.Block))

	if ready, waitFor, err := bc.LagReady(types.Hash{}); !ready || err != nil {
		t.Fatalf("H+1 on the zero parent: ready=%v waitFor=%x err=%v", ready, waitFor[:4], err)
	}
	child := &block.Header{Number: uint256.NewInt(H + 1), ParentHash: hs[2].Hash(), Time: 2010}
	rtx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res, target, err := bc.LaggedExecutedResult(rtx, child, nil)
	rtx.Rollback()
	if err != nil || target.Hash() != hs[1].Hash() || res.Root != hs[2].Root || res.GasUsed != hs[2].GasUsed {
		t.Fatalf("H+1 must carry H-1's result from H's header: %+v err=%v", res, err)
	}
	// H+2 on the available, unexecuted H+1 reports H (stored).
	next := block.NewBlock(child, nil).(*block.Block)
	bc.RememberAvailable(next)
	if ready, waitFor, err := bc.LagReady(next.Hash()); !ready || err != nil {
		t.Fatalf("H+2: ready=%v waitFor=%x err=%v", ready, waitFor[:4], err)
	}
	// Unchanged: an unknown non-zero parent waits on itself.
	unknown := types.Hash{0x77}
	if ready, waitFor, _ := bc.LagReady(unknown); ready || waitFor != unknown {
		t.Fatalf("unknown parent: ready=%v waitFor=%x", ready, waitFor[:4])
	}
	// Once a block is applied here, results come from execution only.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteQMDBApplied(tx, H, hs[2].Hash())
	}); err != nil {
		t.Fatal(err)
	}
	if ready, waitFor, _ := bc.LagReady(types.Hash{}); ready || waitFor != hs[1].Hash() {
		t.Fatalf("with an applied marker the seeded rule must be off: ready=%v waitFor=%x", ready, waitFor[:4])
	}
}
