// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// newFullTestDB opens an in-memory MDBX with BOTH the standard chaindata
// schema (SyncStage, etc.) and the N42 dict tables (AddrDict, DictMeta,
// ...) so a single test DB can exercise handFull's progress+dict-pending
// write path together.
func newFullTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	dictTablesInitOnce.Do(modules.N42Init)
	merged := make(kv.TableCfg, len(kv.ChaindataTablesCfg)+len(modules.N42TableCfg))
	for k, v := range kv.ChaindataTablesCfg {
		merged[k] = v
	}
	for k, v := range modules.N42TableCfg {
		merged[k] = v
	}
	db := mdbx.NewMDBX(log.New()).
		InMem(t.TempDir()).
		MapSize(1 * datasize.GB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return merged }).
		MustOpen()
	t.Cleanup(db.Close)
	return db
}

// TestAsyncFlusher_WaitPrevNoFlightIsNoop confirms waitPrev on a fresh
// flusher (nothing handed off yet) returns immediately with no error.
func TestAsyncFlusher_WaitPrevNoFlightIsNoop(t *testing.T) {
	db := memdb.NewTestDB(t)
	buf := state.NewPlainStateBuffer()
	f := newAsyncFlusher(buf, db, context.Background())

	dur, err := f.waitPrev()
	require.NoError(t, err)
	require.Zero(t, dur)
}

// TestAsyncFlusher_HandAndWaitPrev drives the full background-flush
// handoff: hand() snapshots the buffer and commits progress on a bg
// goroutine; waitPrev() blocks until that completes and surfaces the
// result. Progress must be visible afterwards via ReadProgress.
func TestAsyncFlusher_HandAndWaitPrev(t *testing.T) {
	db := memdb.NewTestDB(t)
	buf := state.NewPlainStateBuffer()
	f := newAsyncFlusher(buf, db, context.Background())

	require.NoError(t, f.hand(42))

	dur, err := f.waitPrev()
	require.NoError(t, err)
	require.GreaterOrEqual(t, dur, time.Duration(0))

	tx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	require.Equal(t, uint64(42), ReadProgress(tx))
}

// TestAsyncFlusher_HandWhileInFlightErrors confirms the "must waitPrev
// first" contract is enforced.
func TestAsyncFlusher_HandWhileInFlightErrors(t *testing.T) {
	db := memdb.NewTestDB(t)
	buf := state.NewPlainStateBuffer()
	f := newAsyncFlusher(buf, db, context.Background())

	require.NoError(t, f.hand(1))
	err := f.hand(2)
	require.Error(t, err)

	// Drain so the test doesn't leak a goroutine/channel send.
	_, err = f.waitPrev()
	require.NoError(t, err)
}

// TestAsyncFlusher_HandWithRemainderPersistsRemainder verifies
// handWithRemainder writes the changeset remainder atomically with the
// state commit, and that it is readable back via ReadCSRemainder.
func TestAsyncFlusher_HandWithRemainderPersistsRemainder(t *testing.T) {
	db := memdb.NewTestDB(t)
	buf := state.NewPlainStateBuffer()
	f := newAsyncFlusher(buf, db, context.Background())

	remainder := map[string][]byte{"acctcs": {1, 2, 3}}
	require.NoError(t, f.handWithRemainder(7, remainder))
	_, err := f.waitPrev()
	require.NoError(t, err)

	tx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	got := ReadCSRemainder(tx)
	require.Equal(t, []byte{1, 2, 3}, got["acctcs"])
}

// TestAsyncFlusher_HandFullWithDictPending exercises the maximal handoff
// path including a non-empty DictPending snapshot.
func TestAsyncFlusher_HandFullWithDictPending(t *testing.T) {
	db := newFullTestDB(t)
	buf := state.NewPlainStateBuffer()
	f := newAsyncFlusher(buf, db, context.Background())

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()
	bw := NewBufferedDictWriter(roTx)

	var addr [20]byte
	addr[19] = 9
	_, err = bw.InternAddr(addr)
	require.NoError(t, err)
	pending := bw.TakePending()
	require.False(t, pending.IsEmpty())

	require.NoError(t, f.handFull(99, nil, pending))
	_, err = f.waitPrev()
	require.NoError(t, err)

	checkTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer checkTx.Rollback()
	require.Equal(t, uint64(99), ReadProgress(checkTx))

	dr, err := NewDictReader(checkTx)
	require.NoError(t, err)
	gotAddr, err := dr.LookupAddr(1)
	require.NoError(t, err)
	require.Equal(t, addr, [20]byte(gotAddr))
}
