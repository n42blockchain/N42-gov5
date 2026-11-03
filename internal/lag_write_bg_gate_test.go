// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// With N42_LAG_WRITE_DEFER_BG on, a busy lag writer defers a fold until a
// full batch is pending; a quiet one lets it run. Deferred, never dropped.
func TestBackfillDefersToBusyLagWriter(t *testing.T) {
	defer SetLagWriteDeferBGForTest(true)()
	prevIdle := lagWriteDeferBGIdle
	lagWriteDeferBGIdle = time.Hour
	defer func() { lagWriteDeferBGIdle = prevIdle; lagBGGate = lagWriteBGGate{} }()

	db := memdb.NewTestDB(t)
	seedChangesets(t, db, 12)
	head := uint64(3)
	b := NewHistoryBackfiller(db, func() uint64 { return head }, 4, time.Hour)
	defer b.Stop()

	// A lag write just ran: 3 blocks behind (< batch 4) is deferred.
	lagBGGate.enter()
	lagBGGate.exit()
	folded, err := b.stepFold()
	require.NoError(t, err)
	require.False(t, folded)
	_, ok := markerOf(t, b)
	require.False(t, ok, "a deferred fold must not move the marker")

	// A full batch pending: folds although the writer is busy.
	head = 12
	folded, err = b.stepFold()
	require.NoError(t, err)
	require.True(t, folded)
	n, _ := markerOf(t, b)
	require.Equal(t, uint64(4), n)

	// The writer quiet: the rest folds.
	lagBGGate = lagWriteBGGate{}
	head = 10
	for {
		folded, err := b.stepFold()
		require.NoError(t, err)
		if !folded {
			break
		}
	}
	n, _ = markerOf(t, b)
	require.Equal(t, uint64(10), n)
}

// A fold's write transaction waits for an open lag write transaction to
// close, at most the configured wait.
func TestLagBGGateWaitClosed(t *testing.T) {
	var g lagWriteBGGate
	require.Less(t, g.waitClosed(time.Second, nil), 10*time.Millisecond)
	g.enter()
	go func() {
		time.Sleep(30 * time.Millisecond)
		g.exit()
	}()
	w := g.waitClosed(time.Second, nil)
	require.GreaterOrEqual(t, w, 20*time.Millisecond)
	require.Less(t, w, 900*time.Millisecond)
	g.enter()
	defer g.exit()
	require.GreaterOrEqual(t, g.waitClosed(20*time.Millisecond, nil), 20*time.Millisecond)
	require.True(t, g.activeWithin(0))
}
