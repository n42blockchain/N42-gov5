// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
)

// TestBufferedDictWriter_InternFlushRoundTrip exercises the pending-map
// buffering path: intern a few addrs/codeHashes against a RoTx, flush into a
// RwTx, and confirm a fresh DictReader resolves the persisted ids.
func TestBufferedDictWriter_InternFlushRoundTrip(t *testing.T) {
	db := newDictTestDB(t)

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	bw := NewBufferedDictWriter(roTx)

	var a1, a2 types.Address
	a1[19] = 1
	a2[19] = 2
	var h1 types.Hash
	h1[31] = 0xAB

	id1, err := bw.InternAddr(a1)
	require.NoError(t, err)
	id2, err := bw.InternAddr(a2)
	require.NoError(t, err)
	require.NotEqual(t, id1, id2)

	// Re-interning the same addr before flush returns the same cached id.
	id1Again, err := bw.InternAddr(a1)
	require.NoError(t, err)
	require.Equal(t, id1, id1Again)

	hid, err := bw.InternCodeHash(h1)
	require.NoError(t, err)
	require.NotZero(t, hid)

	// The zero hash is the "no code" sentinel and must never be interned.
	zid, err := bw.InternCodeHash(types.Hash{})
	require.NoError(t, err)
	require.Zero(t, zid)

	addrsPending, codeHashesPending := bw.PendingCount()
	require.Equal(t, 2, addrsPending)
	require.Equal(t, 1, codeHashesPending)

	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer rwTx.Rollback()

	require.NoError(t, bw.Flush(rwTx))
	require.NoError(t, rwTx.Commit())

	// Pending maps are drained after flush.
	addrsPending, codeHashesPending = bw.PendingCount()
	require.Zero(t, addrsPending)
	require.Zero(t, codeHashesPending)

	// A fresh reader against a new tx must resolve persisted ids.
	checkTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer checkTx.Rollback()

	dr, err := NewDictReader(checkTx)
	require.NoError(t, err)

	gotA1, err := dr.LookupAddr(id1)
	require.NoError(t, err)
	require.Equal(t, a1, gotA1)

	gotH1, err := dr.LookupCodeHash(hid)
	require.NoError(t, err)
	require.Equal(t, h1, gotH1)
}

// TestBufferedDictWriter_FlushEmptyIsNoop confirms Flush on a writer that
// never interned anything is a cheap no-op (no counters touched).
func TestBufferedDictWriter_FlushEmptyIsNoop(t *testing.T) {
	db := newDictTestDB(t)
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	bw := NewBufferedDictWriter(roTx)

	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer rwTx.Rollback()

	require.NoError(t, bw.Flush(rwTx))
}

// TestBufferedDictWriter_BindRoTxAndTakePending verifies BindRoTx swaps the
// backing tx and TakePending/FlushPending snapshots pending state without
// racing the writer's own counters.
func TestBufferedDictWriter_BindRoTxAndTakePending(t *testing.T) {
	db := newDictTestDB(t)

	roTx1, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx1.Rollback()

	bw := NewBufferedDictWriter(roTx1)

	var a1 types.Address
	a1[19] = 7
	_, err = bw.InternAddr(a1)
	require.NoError(t, err)

	pending := bw.TakePending()
	require.False(t, pending.IsEmpty())

	addrsPending, _ := bw.PendingCount()
	require.Zero(t, addrsPending, "TakePending should drain the pending map")

	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer rwTx.Rollback()
	require.NoError(t, FlushPending(rwTx, pending))
	require.NoError(t, rwTx.Commit())

	// Swap in a fresh RoTx (simulating a commit-interval rotation).
	roTx2, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx2.Rollback()
	bw.BindRoTx(roTx2)

	// Resolving the same addr again should hit the in-memory cache, not the
	// (now potentially stale) roTx.
	idAgain, err := bw.InternAddr(a1)
	require.NoError(t, err)
	require.NotZero(t, idAgain)
}

// TestFlushPending_NilSnapshotIsSafe ensures an empty/default snapshot is a
// true no-op and never touches the transaction.
func TestFlushPending_NilSnapshotIsSafe(t *testing.T) {
	empty := &DictPending{}
	require.True(t, empty.IsEmpty())
	require.NoError(t, FlushPending(nil, empty))

	var nilPending *DictPending
	require.True(t, nilPending.IsEmpty())
}

// TestDictWriter_InternAddr_CacheHitAvoidsReallocation drives the eager
// DictWriter's cache path: interning the same addr twice must return the
// identical id without allocating a new one.
func TestDictWriter_InternAddr_CacheHitAvoidsReallocation(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, _ *DictReader) {
		var a types.Address
		a[19] = 42
		id1, err := dw.InternAddr(a)
		require.NoError(t, err)
		id2, err := dw.InternAddr(a)
		require.NoError(t, err)
		require.Equal(t, id1, id2)
	})
}

// TestDictWriter_InternCodeHash_ZeroSentinel confirms the eager writer also
// treats the zero hash as the "no code" sentinel.
func TestDictWriter_InternCodeHash_ZeroSentinel(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, _ *DictReader) {
		id, err := dw.InternCodeHash(types.Hash{})
		require.NoError(t, err)
		require.Zero(t, id)
	})
}
