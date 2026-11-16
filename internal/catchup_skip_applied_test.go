// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type joinAttempted struct{ number uint64 }

// skipAppliedFixture: blocks 9..12, 9 and 10 stored and 10 the durable
// applied head, 11 the write in flight, 12 nowhere.
func skipAppliedFixture(t *testing.T) (*BlockChain, []block.IBlock) {
	t.Helper()
	db := newRealignTestDB(t)
	var blocks []block.IBlock
	parent := types.Hash{0x09}
	for n := uint64(9); n <= 12; n++ {
		h := &block.Header{Number: uint256.NewInt(n), ParentHash: parent, Difficulty: uint256.NewInt(1)}
		b := block.NewBlock(h, nil)
		blocks = append(blocks, b)
		parent = b.Hash()
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for _, b := range blocks[:2] {
			rawdb.WriteHeader(tx, b.Header().(*block.Header))
		}
		return rawdb.WriteQMDBApplied(tx, 10, blocks[1].Hash())
	}); err != nil {
		t.Fatal(err)
	}
	bc := &BlockChain{ChainDB: db, ctx: context.Background(), qmdbEnabled: true, engine: transferTestEngine{}}
	j := &lagWriteJob{hash: blocks[2].Hash(), number: 11, done: make(chan struct{})}
	bc.lagWrite.inflight = j
	bc.lagWrite.visible.Store(j)
	return bc, blocks
}

// insertJoins runs insertChainMode and reports whether it started waiting
// for the write in flight (the test hook stops it there).
func insertJoins(t *testing.T, bc *BlockChain, chain []block.IBlock) (joined bool, n int, err error) {
	t.Helper()
	lagWriteJoinForTest = func(number uint64) { panic(joinAttempted{number}) }
	defer func() { lagWriteJoinForTest = nil }()
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(joinAttempted); !ok {
				panic(r)
			}
			joined = true
		}
	}()
	n, err = bc.insertChainMode(chain, true, nil)
	return false, n, err
}

func TestInsertSkipsJoinForAppliedOrInFlight(t *testing.T) {
	defer SetCatchUpSkipAppliedForTest(true)()
	bc, b := skipAppliedFixture(t)
	for name, chain := range map[string][]block.IBlock{
		"durably applied":       b[1:2],
		"in flight":             b[2:3],
		"applied then inflight": b[0:3],
	} {
		skipped0, _ := InsertJoinStats()
		joined, n, err := insertJoins(t, bc, chain)
		if joined || err != nil || n != len(chain) {
			t.Fatalf("%s: joined %v, (%d, %v), want (%d, nil) without a join", name, joined, n, err, len(chain))
		}
		if skipped, _ := InsertJoinStats(); skipped != skipped0+1 {
			t.Fatalf("%s: skip not counted", name)
		}
	}
}

// A block that is neither applied nor in flight joins and imports as today,
// also when an applied block precedes it in the chain.
func TestInsertJoinsForMissingBlock(t *testing.T) {
	defer SetCatchUpSkipAppliedForTest(true)()
	bc, b := skipAppliedFixture(t)
	for name, chain := range map[string][]block.IBlock{
		"missing":             b[3:4],
		"inflight then child": b[2:4],
	} {
		if joined, _, _ := insertJoins(t, bc, chain); !joined {
			t.Fatalf("%s: did not join the write in flight", name)
		}
	}
}

func TestInsertJoinsWithSwitchOff(t *testing.T) {
	defer SetCatchUpSkipAppliedForTest(false)()
	bc, b := skipAppliedFixture(t)
	if joined, _, _ := insertJoins(t, bc, b[2:3]); !joined {
		t.Fatal("switch off: the in-flight block's import did not join")
	}
}

// The lag executor's own imports never take the shortcut.
func TestInsertSkipIgnoresLagImports(t *testing.T) {
	defer SetCatchUpSkipAppliedForTest(true)()
	bc, b := skipAppliedFixture(t)
	if bc.chainAppliedOrInFlightLocked(b[1:2]) != true {
		t.Fatal("fixture: block 10 should read as applied")
	}
	lagWriteJoinForTest = func(number uint64) { panic(joinAttempted{number}) }
	defer func() { lagWriteJoinForTest = nil }()
	joined := func() (j bool) {
		defer func() {
			if r := recover(); r != nil {
				j = true
			}
		}()
		_, _ = bc.insertChainMode(b[1:2], false, &lagInsert{})
		return false
	}()
	if !joined {
		t.Fatal("a lag import skipped the join")
	}
}

func TestLagPipelineHolds(t *testing.T) {
	bc, b := skipAppliedFixture(t)
	if !bc.LagPipelineHolds(b[2].Hash(), 11) {
		t.Fatal("the write in flight is not held")
	}
	if bc.LagPipelineHolds(b[2].Hash(), 12) || bc.LagPipelineHolds(b[3].Hash(), 12) {
		t.Fatal("a block outside the pipeline is held")
	}
	bc.lagExec.mu.Lock()
	bc.lagExec.queued = map[types.Hash]*lagQueued{b[3].Hash(): nil}
	bc.lagExec.mu.Unlock()
	if !bc.LagPipelineHolds(b[3].Hash(), 12) {
		t.Fatal("a queued block is not held")
	}
}

// Without a write in flight nothing is skipped: the import runs as today.
func TestChainAppliedOrInFlightNeedsAWrite(t *testing.T) {
	bc, b := skipAppliedFixture(t)
	bc.lagWrite.inflight = nil
	if bc.chainAppliedOrInFlightLocked(b[1:2]) {
		t.Fatal("skip allowed without a write in flight")
	}
}
