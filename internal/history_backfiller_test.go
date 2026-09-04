// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// seedChangesets writes real changeset rows for blocks 1..n through the same
// writer the commit path uses, so the backfiller reads what a node would
// actually have produced rather than a hand-rolled approximation.
func seedChangesets(t *testing.T, db kv.RwDB, n uint64) {
	t.Helper()
	for i := uint64(1); i <= n; i++ {
		require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
			w := state.NewPlainStateWriter(tx, tx, i)
			var addr types.Address
			addr[19] = byte(i)
			before := account.NewAccount()
			after := account.NewAccount()
			after.Nonce = i
			after.Balance = *uint256.NewInt(i)
			if err := w.UpdateAccountData(addr, &before, &after); err != nil {
				return err
			}
			return w.WriteChangeSets() // changesets only: the index is what we are backfilling
		}))
	}
}

func countRows(t *testing.T, db kv.RwDB, table string) int {
	t.Helper()
	n := 0
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		c, err := tx.Cursor(table)
		if err != nil {
			return err
		}
		defer c.Close()
		for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
			if err != nil {
				return err
			}
			n++
		}
		return nil
	}))
	return n
}

func markerOf(t *testing.T, b *HistoryBackfiller) (uint64, bool) {
	t.Helper()
	return b.IndexedThrough()
}

// The batch cap must hold: one step may not cover more blocks than it was
// asked to, or a node that has fallen far behind builds one unbounded
// transaction and stalls the write lock it shares with block import.
func TestBackfillRespectsBatchAndResumes(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, 10)

	b := NewHistoryBackfiller(db, func() uint64 { return 10 }, 4, time.Hour)
	defer b.Stop()

	if n, ok := markerOf(t, b); ok || n != 0 {
		t.Fatalf("a fresh database must report NOTHING indexed, got n=%d ok=%v; "+
			"reading a missing marker as 'everything' would let queries into an empty index", n, ok)
	}

	require.NoError(t, b.step())
	n, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, uint64(4), n, "one step must stop at the batch edge")
	require.Positive(t, countRows(t, db, modules.AccountsHistory), "the index must actually have rows")

	require.NoError(t, b.step())
	n, _ = markerOf(t, b)
	require.Equal(t, uint64(8), n, "the second step must resume from the marker, not restart")

	require.NoError(t, b.step())
	n, _ = markerOf(t, b)
	require.Equal(t, uint64(10), n, "the last step must stop at the head, not past it")

	require.NoError(t, b.step())
	n, _ = markerOf(t, b)
	require.Equal(t, uint64(10), n, "with nothing new the marker must not move")
}

// The marker may lag the rows (harmless, the range rebuilds) but must never
// lead them: a marker ahead of the index lets a historical query read a gap as
// "untouched" and fall back to the current value. They are written in one
// transaction, so this asserts the invariant that structure is meant to give.
func TestBackfillMarkerNeverLeadsTheRows(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, 6)
	b := NewHistoryBackfiller(db, func() uint64 { return 6 }, 100, time.Hour)
	defer b.Stop()

	require.NoError(t, b.step())
	marker, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, uint64(6), marker)
	require.Positive(t, countRows(t, db, modules.AccountsHistory),
		"the marker claims blocks are covered, so the index must be non-empty")
}

// A head below the marker (a rewind) must not walk backwards or re-flush.
func TestBackfillIsQuietWhenHeadIsBehind(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, 5)
	b := NewHistoryBackfiller(db, func() uint64 { return 5 }, 100, time.Hour)
	defer b.Stop()
	require.NoError(t, b.step())
	before, _ := markerOf(t, b)

	b2 := NewHistoryBackfiller(db, func() uint64 { return 2 }, 100, time.Hour)
	defer b2.Stop()
	require.NoError(t, b2.step())
	after, _ := markerOf(t, b2)
	require.Equal(t, before, after, "a head behind the marker must leave it alone")
}

// Stop must not wait on a loop that was never launched. A node that builds a
// backfiller and then returns on an earlier startup error would otherwise pay
// the full timeout on every shutdown.
func TestStopWithoutStartReturnsImmediately(t *testing.T) {
	db := memdb.NewTestDB(t)
	b := NewHistoryBackfiller(db, func() uint64 { return 0 }, 8, time.Hour)
	start := time.Now()
	b.Stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Stop without Start took %v; it must return at once", d)
	}
}

// And Start must be idempotent, or a retried startup path spawns two loops
// racing on the same marker.
func TestStartIsIdempotent(t *testing.T) {
	db := memdb.NewTestDB(t)
	b := NewHistoryBackfiller(db, func() uint64 { return 0 }, 8, time.Hour)
	b.Start()
	b.Start()
	b.Stop()
}

// indexCoversBlock reports whether the inverted index actually contains an
// entry naming this block. The marker claims coverage; this checks it.
func indexCoversBlock(t *testing.T, db kv.RwDB, blockNum uint64) bool {
	t.Helper()
	var addr types.Address
	addr[19] = byte(blockNum)
	found := false
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.AccountsHistory)
		if err != nil {
			return err
		}
		defer c.Close()
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				return err
			}
			if len(k) >= 20 && types.BytesToAddress(k[:20]) == addr && len(v) > 0 {
				found = true
				return nil
			}
		}
		return nil
	}))
	return found
}

// A marker that advances past work not actually written is how the sibling
// accthist/storhist builder sat frozen for three months with no error and no
// gap report — flagged by the DATC archive session, which hit exactly that.
// Here the flush and the marker share ONE transaction, so a failed step must
// move neither. This kills the step mid-flight (context cancellation is the
// in-process stand-in for a crash) and asserts the marker stayed put.
func TestFailedStepLeavesTheMarkerWhereItWas(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, 12)

	b := NewHistoryBackfiller(db, func() uint64 { return 12 }, 4, time.Hour)
	require.NoError(t, b.step())
	before, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, uint64(4), before)

	// Kill it. The next step must fail and change nothing.
	b.cancel()
	err := b.step()
	require.Error(t, err, "a step on a cancelled context must fail rather than half-commit")

	// Read the marker with a LIVE context: b.IndexedThrough uses the
	// backfiller's own (now cancelled) context and would report (0, false),
	// which is its fail-closed answer for "cannot establish coverage" and not
	// evidence about what is on disk.
	var after uint64
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		var e error
		after, _, e = rawdb.ReadHistoryIndexedThrough(tx)
		return e
	}))
	require.Equal(t, before, after,
		"the marker moved on a step that did not complete; a marker ahead of the rows is how a "+
			"historical query reads a gap as 'untouched' and answers with the current value")
}

// The positive half of the same invariant: every block the marker claims must
// actually be present in the index. Asserting only that the marker did not
// move would pass for a backfiller that never writes anything.
func TestMarkerOnlyClaimsBlocksTheIndexActuallyHas(t *testing.T) {
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, 6)
	b := NewHistoryBackfiller(db, func() uint64 { return 6 }, 3, time.Hour)
	defer b.Stop()

	require.NoError(t, b.step())
	marker, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, uint64(3), marker)

	for i := uint64(1); i <= marker; i++ {
		require.True(t, indexCoversBlock(t, db, i),
			"block %d is at or below the marker but the index has no entry for it", i)
	}
	require.False(t, indexCoversBlock(t, db, marker+1),
		"block %d is above the marker and must NOT be indexed yet, or the batch cap is not holding", marker+1)
}
