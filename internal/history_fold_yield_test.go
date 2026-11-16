// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// foldAll runs one backfill step over blocks seeded changesets and returns
// both history tables and the marker.
func foldAll(t *testing.T, blocks uint64, between func() error) (acc, sto map[string]string, marker uint64, ok bool) {
	t.Helper()
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, blocks)
	b := NewHistoryBackfiller(db, func() uint64 { return blocks }, 1000, time.Hour)
	historyFoldBetweenTxnsForTest = between
	defer func() { historyFoldBetweenTxnsForTest = nil }()
	require.NoError(t, b.step())
	marker, ok = markerOf(t, b)
	return dumpTable(t, db, modules.AccountsHistory), dumpTable(t, db, modules.StorageHistory), marker, ok
}

// TestHistoryFoldYieldSameTables (N42_HISTORY_FOLD_YIELD): the fold writes
// the same AccountsHistory / StorageHistory rows and marker with the switch
// off and on, also when every transaction yields to a waiting block write
// after one key (no-sync commits, one key each).
func TestHistoryFoldYieldSameTables(t *testing.T) {
	defer SetLagWriteDeferBGForTest(false)() // its start deferral is not under test here
	const blocks = 40
	defer SetHistoryFoldYieldForTest(false)()
	wantAcc, wantSto, wantMarker, ok := foldAll(t, blocks, nil)
	require.True(t, ok)
	require.Len(t, wantAcc, blocks)

	SetHistoryFoldYieldForTest(true)
	acc, sto, marker, ok := foldAll(t, blocks, nil)
	require.True(t, ok)
	require.Equal(t, wantMarker, marker)
	require.Equal(t, wantAcc, acc)
	require.Equal(t, wantSto, sto)

	// A block write waits during every fold transaction: each puts one key.
	prev := historyFoldWriterWanted
	historyFoldWriterWanted = func() bool { return true }
	defer func() { historyFoldWriterWanted = prev }()
	txns := 0
	acc, sto, marker, ok = foldAll(t, blocks, func() error { txns++; return nil })
	require.True(t, ok)
	require.Equal(t, blocks-1, txns, "one key per yielding transaction")
	require.Equal(t, wantMarker, marker)
	require.Equal(t, wantAcc, acc)
	require.Equal(t, wantSto, sto)
}

// TestHistoryFoldYieldCrashResumes: a yielding fold interrupted between two
// of its transactions leaves the marker where it was; the next step folds
// again to the same rows and marker as an uninterrupted fold.
func TestHistoryFoldYieldCrashResumes(t *testing.T) {
	defer SetLagWriteDeferBGForTest(false)() // its start deferral is not under test here
	const blocks = 24
	defer SetHistoryFoldYieldForTest(false)()
	wantAcc, _, wantMarker, _ := foldAll(t, blocks, nil)

	SetHistoryFoldYieldForTest(true)
	prev := historyFoldWriterWanted
	historyFoldWriterWanted = func() bool { return true }
	defer func() { historyFoldWriterWanted = prev }()
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, blocks)
	b := NewHistoryBackfiller(db, func() uint64 { return blocks }, 1000, time.Hour)
	n := 0
	historyFoldBetweenTxnsForTest = func() error {
		n++
		if n == 5 {
			return context.Canceled
		}
		return nil
	}
	defer func() { historyFoldBetweenTxnsForTest = nil }()
	require.Error(t, b.step())
	_, ok := markerOf(t, b)
	require.False(t, ok, "the marker must not move before the last rows")
	require.Len(t, dumpTable(t, db, modules.AccountsHistory), 5)
	historyFoldBetweenTxnsForTest = nil
	require.NoError(t, b.step())
	require.Equal(t, wantAcc, dumpTable(t, db, modules.AccountsHistory))
	m, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, wantMarker, m)
}

// TestHistoryFoldYieldWaitsForBlockWrite: a fold transaction does not begin
// while an ordered block write is open or waiting (the gate), and starts
// once it has committed.
func TestHistoryFoldYieldWaitsForBlockWrite(t *testing.T) {
	defer SetLagWriteDeferBGForTest(false)() // its start deferral is not under test here
	const blocks = 8
	defer SetHistoryFoldYieldForTest(true)()
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, blocks)
	b := NewHistoryBackfiller(db, func() uint64 { return blocks }, 1000, time.Hour)
	lagBGGate.enter()
	released := make(chan time.Time, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		released <- time.Now()
		lagBGGate.exit()
	}()
	require.NoError(t, b.step())
	done := time.Now()
	require.False(t, done.Before(<-released), "the fold wrote while a block write held the gate")
	m, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, uint64(blocks), m)
}

// nosyncCountingDB counts the store's no-sync write transactions.
type nosyncCountingDB struct {
	kv.RwDB
	nosync int
}

func (d *nosyncCountingDB) UpdateNosync(ctx context.Context, f func(tx kv.RwTx) error) error {
	d.nosync++
	return d.RwDB.UpdateNosync(ctx, f)
}

// TestHistoryBackfillYieldWriterSynced (N42_HISTORY_BACKFILL_YIELD_WRITER):
// the fold yields like N42_HISTORY_FOLD_YIELD -- one key per transaction
// while a block write waits -- and writes the same rows and marker, but
// every transaction commits with its data sync.
func TestHistoryBackfillYieldWriterSynced(t *testing.T) {
	defer SetLagWriteDeferBGForTest(false)()
	defer SetHistoryFoldYieldForTest(false)()
	const blocks = 40
	wantAcc, wantSto, wantMarker, ok := foldAll(t, blocks, nil)
	require.True(t, ok)

	defer SetHistoryBackfillYieldWriterForTest(true)()
	prev := historyFoldWriterWanted
	historyFoldWriterWanted = func() bool { return true }
	defer func() { historyFoldWriterWanted = prev }()
	db := &nosyncCountingDB{RwDB: memdb.NewTestDB(t)}
	seedChangesets(t, db.RwDB, blocks)
	b := NewHistoryBackfiller(db, func() uint64 { return blocks }, 1000, time.Hour)
	txns := 0
	historyFoldBetweenTxnsForTest = func() error { txns++; return nil }
	defer func() { historyFoldBetweenTxnsForTest = nil }()
	require.NoError(t, b.step())
	require.Equal(t, blocks-1, txns, "one key per yielding transaction")
	require.Zero(t, db.nosync, "a fold transaction committed without its data sync")
	require.Equal(t, wantAcc, dumpTable(t, db, modules.AccountsHistory))
	require.Equal(t, wantSto, dumpTable(t, db, modules.StorageHistory))
	m, ok := markerOf(t, b)
	require.True(t, ok)
	require.Equal(t, wantMarker, m)

	// With N42_HISTORY_FOLD_YIELD also on, its no-sync commit applies.
	defer SetHistoryFoldYieldForTest(true)()
	if y, ns := historyFoldYields(); !y || !ns {
		t.Fatalf("historyFoldYields() = %v, %v with both on", y, ns)
	}
}

// The new switch alone makes the fold yield, with synced commits.
func TestHistoryBackfillYieldWriterYields(t *testing.T) {
	defer SetHistoryFoldYieldForTest(false)()
	defer SetHistoryBackfillYieldWriterForTest(true)()
	if y, ns := historyFoldYields(); !y || ns {
		t.Fatalf("historyFoldYields() = %v, %v, want true, false", y, ns)
	}
}
