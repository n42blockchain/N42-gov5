// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestLagWriteAnnounceLifecycle: raise / release are idempotent, a
// never-raised announcement is retired without touching the gate, and the
// gate counts exactly the raised and not yet released ones.
func TestLagWriteAnnounceLifecycle(t *testing.T) {
	defer SetLagWriteAnnounceForTest("check")()
	defer func() { lagBGGate = lagWriteBGGate{} }()
	lagBGGate = lagWriteBGGate{} // the gate is process-wide

	a := &lagWriteAnnouncement{}
	require.False(t, lagBGGate.wanted())
	a.raise()
	a.raise()
	require.EqualValues(t, 1, lagBGGate.announced.Load())
	require.True(t, lagBGGate.wanted())
	time.Sleep(time.Millisecond)
	require.Positive(t, a.release())
	require.Zero(t, a.release())
	require.EqualValues(t, 0, lagBGGate.announced.Load())
	a.raise() // after a release: stays down
	require.EqualValues(t, 0, lagBGGate.announced.Load())

	b := &lagWriteAnnouncement{}
	require.Zero(t, b.release())
	b.raise()
	require.EqualValues(t, 0, lagBGGate.announced.Load(), "a retired announcement never goes up")

	var nilA *lagWriteAnnouncement
	nilA.raise()
	require.Zero(t, nilA.release())

	// Ignored (fold too far behind): up, but not wanted.
	c := &lagWriteAnnouncement{}
	c.raise()
	lagBGGate.announceIgnore.Store(true)
	require.False(t, lagBGGate.wanted())
	lagBGGate.announceIgnore.Store(false)
	require.True(t, lagBGGate.wanted())
	c.release()
}

// TestLagWriteAnnounceCheckNesting: in check mode three announcements of
// one chain up at once are a violation (one import, one write in flight).
func TestLagWriteAnnounceCheckNesting(t *testing.T) {
	defer SetLagWriteAnnounceForTest("check")()
	defer func() { lagBGGate = lagWriteBGGate{} }()
	var up atomic.Int32
	a, b, c := &lagWriteAnnouncement{up: &up}, &lagWriteAnnouncement{up: &up}, &lagWriteAnnouncement{up: &up}
	a.raise()
	b.raise() // the import's and the write in flight's
	require.Panics(t, c.raise)
	a.release()
	b.release()
	c.release()
}

// TestLagBGGateWaitClearBounded: an announcement defers a fold's begin while
// it is up, at most annMax in all; an open write is waited for as before.
func TestLagBGGateWaitClearBounded(t *testing.T) {
	defer func() { lagBGGate = lagWriteBGGate{} }()
	done := make(chan struct{})

	// Nothing up: no wait.
	w, ann := lagBGGate.waitClear(time.Second, 50*time.Millisecond, done)
	require.Less(t, w, 5*time.Millisecond)
	require.Zero(t, ann)

	// Announced for good: deferred annMax, then begins.
	lagBGGate.announced.Store(1)
	w, ann = lagBGGate.waitClear(time.Second, 30*time.Millisecond, done)
	require.GreaterOrEqual(t, ann, 30*time.Millisecond)
	require.Less(t, w, 500*time.Millisecond, "bounded by annMax, not by max")

	// annMax 0 (switch off): announcements are not waited for.
	w, _ = lagBGGate.waitClear(time.Second, 0, done)
	require.Less(t, w, 5*time.Millisecond)

	// Ignored: not waited for.
	lagBGGate.announceIgnore.Store(true)
	w, _ = lagBGGate.waitClear(time.Second, 30*time.Millisecond, done)
	require.Less(t, w, 5*time.Millisecond)
	lagBGGate.announceIgnore.Store(false)

	// Released while waiting: begins right after.
	go func() {
		time.Sleep(10 * time.Millisecond)
		lagBGGate.announced.Store(0)
	}()
	w, ann = lagBGGate.waitClear(time.Second, time.Second, done)
	require.GreaterOrEqual(t, w, 10*time.Millisecond)
	require.Less(t, ann, 500*time.Millisecond)
}

// TestHistoryFoldUnderPermanentAnnouncement: a yielding fold under an
// announcement that never comes down (a stream of announced writes) still
// completes, each transaction deferred at most annMax, with the same rows
// and marker as a fold without announcements; and a fold too far behind
// ignores the announcement entirely.
func TestHistoryFoldUnderPermanentAnnouncement(t *testing.T) {
	defer SetLagWriteDeferBGForTest(false)()
	defer SetHistoryBackfillYieldWriterForTest(true)()
	defer func() { lagBGGate = lagWriteBGGate{} }()
	const blocks = 12

	defer SetLagWriteAnnounceForTest("")()
	wantAcc, wantSto, wantMarker, ok := foldAll(t, blocks, nil)
	require.True(t, ok)

	SetLagWriteAnnounceForTest("1")
	prevDefer := lagWriteAnnounceMaxDefer
	lagWriteAnnounceMaxDefer = 5 * time.Millisecond
	defer func() { lagWriteAnnounceMaxDefer = prevDefer }()
	lagBGGate.announced.Store(1)
	txns := 0
	t0 := time.Now()
	acc, sto, marker, ok := foldAll(t, blocks, func() error { txns++; return nil })
	took := time.Since(t0)
	require.True(t, ok)
	require.Equal(t, wantMarker, marker)
	require.Equal(t, wantAcc, acc)
	require.Equal(t, wantSto, sto)
	// One key per transaction (the announcement makes each yield), each
	// begin deferred <= annMax (+ the 5 ms pause between transactions).
	require.Equal(t, blocks-1, txns)
	require.Less(t, took, time.Duration(blocks)*(lagWriteAnnounceMaxDefer+historyFoldYield+50*time.Millisecond))

	// Too far behind: the announcement is ignored, no deferral.
	prevLag := lagWriteAnnounceMaxLag
	lagWriteAnnounceMaxLag = 1
	defer func() { lagWriteAnnounceMaxLag = prevLag }()
	db := memdb.NewTestDB(t)
	seedChangesets(t, db, blocks)
	b := NewHistoryBackfiller(db, func() uint64 { return blocks }, 1000, time.Hour)
	require.NoError(t, b.step())
	require.True(t, lagBGGate.announceIgnore.Load())
	require.Equal(t, wantAcc, dumpTable(t, db, modules.AccountsHistory))
}

// TestLagWriteAnnounceSameRows (N42_LAG_WRITE_ANNOUNCE): the ordered-writer
// pipeline harness (ahead runs, slow writes, an injected failure) ends with
// exactly the same rows in every table with the switch off, on and check;
// with it on every handed-off write was announced and every announcement
// came down (check mode panics when they pile up).
func TestLagWriteAnnounceSameRows(t *testing.T) {
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	defer SetLagHandoffLeanForTest(true)()
	defer SetHistoryBackfillYieldWriterForTest(true)()
	lagWriteBeforeCommitForTest = func(uint64) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()
	run := func(mode string) map[string]string {
		defer SetLagWriteAnnounceForTest(mode)()
		r0, l0 := LagWriteAnnounceStats()
		tables := r68aRunTables(t)
		r1, l1 := LagWriteAnnounceStats()
		t.Logf("announce=%q: raised %d", mode, r1-r0)
		require.Zero(t, l1-l0)
		if mode == "" {
			require.Zero(t, r1-r0)
		} else {
			require.GreaterOrEqual(t, r1-r0, uint64(8), "handed-off writes announced")
		}
		require.Eventually(t, func() bool { return lagBGGate.announced.Load() == 0 }, 5*time.Second, time.Millisecond,
			"an announcement stayed up")
		return tables
	}
	want := run("")
	requireSameTables(t, want, run("1"), "announce on")
	requireSameTables(t, want, run("check"), "announce check")
}
