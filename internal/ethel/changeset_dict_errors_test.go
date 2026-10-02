// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// Note: the GetOne/Get error branches in InternAddr, InternCodeHash,
// ensureCountersLocked, and LookupAddr/LookupCodeHash wrap MDBX cursor
// errors that cannot be triggered through the public kv.Tx API without
// corrupting process state (a rolled-back or closed transaction panics
// inside mdbx-go's cursor open, rather than returning a Go error) — see
// erigontech/mdbx-go@v0.41.0 mdbx/cursor.go:109. Those branches are left
// uncovered for that reason; the reachable branches (not-found, size
// checks, counter resets, exhaustion) are covered below.

// seedCounter writes counterKey = val directly into DictMeta so allocID
// sees a specific high-water mark without actually interning that many
// entries.
func seedCounter(t *testing.T, tx kv.RwTx, counterKey string, val uint32) {
	t.Helper()
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], val)
	require.NoError(t, tx.Put(modules.DictMeta, []byte(counterKey), buf[:]))
}

// TestDictWriter_InternAddr_DictFull covers allocID's "dict full" error
// branch by pre-seeding the counter at MaxDictID.
func TestDictWriter_InternAddr_DictFull(t *testing.T) {
	withDictTx(t, func(t *testing.T, tx kv.RwTx, dw *DictWriter, _ *DictReader) {
		seedCounter(t, tx, addrCounterKey, MaxDictID)
		var a types.Address
		a[19] = 9
		_, err := dw.InternAddr(a)
		require.Error(t, err)
		require.Contains(t, err.Error(), "full")
	})
}

// TestDictWriter_InternCodeHash_DictFull mirrors the above for codeHash ids.
func TestDictWriter_InternCodeHash_DictFull(t *testing.T) {
	withDictTx(t, func(t *testing.T, tx kv.RwTx, dw *DictWriter, _ *DictReader) {
		seedCounter(t, tx, codeHashCounterKey, MaxDictID)
		var h types.Hash
		h[31] = 9
		_, err := dw.InternCodeHash(h)
		require.Error(t, err)
		require.Contains(t, err.Error(), "full")
	})
}

// TestDictWriter_AllocID_CounterZeroResetsToOne covers the branch where a
// persisted counter value of 0 is treated as 1 (defensive against a stray
// zero write).
func TestDictWriter_AllocID_CounterZeroResetsToOne(t *testing.T) {
	withDictTx(t, func(t *testing.T, tx kv.RwTx, dw *DictWriter, _ *DictReader) {
		seedCounter(t, tx, addrCounterKey, 0)
		var a types.Address
		a[19] = 3
		id, err := dw.InternAddr(a)
		require.NoError(t, err)
		require.Equal(t, uint32(1), id)
	})
}

// TestBufferedDictWriter_DictFull covers both addr and codeHash "dict full"
// branches by seeding the persisted counters before interning.
func TestBufferedDictWriter_DictFull(t *testing.T) {
	db := newDictTestDB(t)

	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	seedCounter(t, rwTx, addrCounterKey, MaxDictID)
	seedCounter(t, rwTx, codeHashCounterKey, MaxDictID)
	require.NoError(t, rwTx.Commit())

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	bw := NewBufferedDictWriter(roTx)

	var a types.Address
	a[19] = 1
	_, err = bw.InternAddr(a)
	require.Error(t, err)
	require.Contains(t, err.Error(), "full")

	var h types.Hash
	h[31] = 1
	_, err = bw.InternCodeHash(h)
	require.Error(t, err)
	require.Contains(t, err.Error(), "full")
}

// TestDictReader_LookupAddr_SizeMismatch covers the defensive size check
// when the stored value under AddrDict isn't a 20-byte address.
func TestDictReader_LookupAddr_SizeMismatch(t *testing.T) {
	db := newDictTestDB(t)
	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	var idBuf [3]byte
	putBE24(idBuf[:], 5)
	require.NoError(t, rwTx.Put(modules.AddrDict, idBuf[:], []byte{1, 2, 3}))
	require.NoError(t, rwTx.Commit())

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	dr, err := NewDictReader(roTx)
	require.NoError(t, err)
	_, err = dr.LookupAddr(5)
	require.Error(t, err)
	require.Contains(t, err.Error(), "want 20")
}

// TestDictReader_LookupCodeHash_SizeMismatch mirrors the above for the
// 32-byte codeHash dictionary.
func TestDictReader_LookupCodeHash_SizeMismatch(t *testing.T) {
	db := newDictTestDB(t)
	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	var idBuf [3]byte
	putBE24(idBuf[:], 7)
	require.NoError(t, rwTx.Put(modules.CodeHashDict, idBuf[:], []byte{9, 9}))
	require.NoError(t, rwTx.Commit())

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	dr, err := NewDictReader(roTx)
	require.NoError(t, err)
	_, err = dr.LookupCodeHash(7)
	require.Error(t, err)
	require.Contains(t, err.Error(), "want 32")
}

// TestDictReader_LookupAddr_ReservedSentinel covers LookupAddr(0)'s
// reserved-id error, and LookupCodeHash(0)'s "no code" short-circuit.
func TestDictReader_LookupAddr_ReservedSentinel(t *testing.T) {
	db := newDictTestDB(t)
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	dr, err := NewDictReader(roTx)
	require.NoError(t, err)

	_, err = dr.LookupAddr(0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reserved")

	h, err := dr.LookupCodeHash(0)
	require.NoError(t, err)
	require.Equal(t, types.Hash{}, h)
}

// TestNewDictReaderSized_DefaultsOnNonPositive covers the <=0 fallback
// branches for both LRU capacities.
func TestNewDictReaderSized_DefaultsOnNonPositive(t *testing.T) {
	db := newDictTestDB(t)
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	dr, err := NewDictReaderSized(roTx, 0, -5)
	require.NoError(t, err)
	require.NotNil(t, dr)
}

// TestFlushPending_PartialSnapshots covers the independent addr-only and
// codeHash-only branches inside FlushPending's two for-loops, plus the
// HasCounters-false path where counters are left untouched.
func TestFlushPending_PartialSnapshots(t *testing.T) {
	db := newDictTestDB(t)

	var a types.Address
	a[19] = 11
	addrOnly := &DictPending{
		Addrs:       map[types.Address]uint32{a: 1},
		HasCounters: false,
	}
	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	require.NoError(t, FlushPending(rwTx, addrOnly))
	require.NoError(t, rwTx.Commit())

	var h types.Hash
	h[31] = 22
	codeHashOnly := &DictPending{
		CodeHashes:   map[types.Hash]uint32{h: 2},
		HasCounters:  true,
		AddrNext:     5,
		CodeHashNext: 6,
	}
	rwTx2, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	require.NoError(t, FlushPending(rwTx2, codeHashOnly))
	require.NoError(t, rwTx2.Commit())

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()
	dr, err := NewDictReader(roTx)
	require.NoError(t, err)

	gotA, err := dr.LookupAddr(1)
	require.NoError(t, err)
	require.Equal(t, a, gotA)

	gotH, err := dr.LookupCodeHash(2)
	require.NoError(t, err)
	require.Equal(t, h, gotH)
}

// TestDictWriter_ConcurrentInternAddr_SameValue exercises the mutex-guarded
// cache path with concurrent callers interning the same address; all must
// agree on one id (serializing through w.mu, no duplicate allocation).
func TestDictWriter_ConcurrentInternAddr_SameValue(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, _ *DictReader) {
		var a types.Address
		a[19] = 77
		ids := make([]uint32, 8)
		done := make(chan int, 8)
		for i := 0; i < 8; i++ {
			i := i
			go func() {
				id, err := dw.InternAddr(a)
				require.NoError(t, err)
				ids[i] = id
				done <- i
			}()
		}
		for i := 0; i < 8; i++ {
			<-done
		}
		for _, id := range ids {
			require.Equal(t, ids[0], id)
		}
	})
}
