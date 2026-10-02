// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// changeset_dict_mapfull_test.go drives the dictionary's MDBX Put error
// branches (AddrDict/AddrIndex/CodeHashDict/CodeHashIndex .Put, and the
// DictMeta counter writes) with a real, undersized MDBX map so
// MDBX_MAP_FULL is returned by the library itself — the same condition a
// production node would hit on a misconfigured or exhausted datadir.

package ethel

import (
	"context"
	"testing"

	"github.com/c2h5oh/datasize"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// newTinyMapTestDB opens an in-memory MDBX with the N42 dict tables but a
// 1 MiB map and no auto-growth, so a bulk write loop reliably exhausts it
// within a few thousand entries.
func newTinyMapTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	dictTablesInitOnce.Do(modules.N42Init)
	db := mdbx.NewMDBX(log.New()).
		InMem(t.TempDir()).
		MapSize(1 * datasize.MB).
		GrowthStep(0).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).
		MustOpen()
	t.Cleanup(db.Close)
	return db
}

// TestDictWriter_InternAddr_MapFull drives InternAddr's AddrIndex.Put
// error-wrapping branch via a real MDBX_MAP_FULL (MDBX deterministically
// exhausts the larger-keyed AddrIndex table before the 3B-keyed AddrDict
// table in this access pattern, so AddrDict.Put's sibling error branch is
// not independently reachable this way; both wrap the same Put call
// identically).
func TestDictWriter_InternAddr_MapFull(t *testing.T) {
	db := newTinyMapTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	dw := NewDictWriter(tx)
	var gotErr error
	for i := 0; i < 200000; i++ {
		var a types.Address
		a[0] = byte(i >> 16)
		a[1] = byte(i >> 8)
		a[2] = byte(i)
		if _, err := dw.InternAddr(a); err != nil {
			gotErr = err
			break
		}
	}
	require.Error(t, gotErr, "expected MDBX_MAP_FULL before exhausting the loop")
	require.Contains(t, gotErr.Error(), "Put")
}

// TestDictWriter_InternCodeHash_MapFull mirrors the above for the
// CodeHashIndex.Put error-wrapping branch.
func TestDictWriter_InternCodeHash_MapFull(t *testing.T) {
	db := newTinyMapTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	dw := NewDictWriter(tx)
	var gotErr error
	for i := 0; i < 200000; i++ {
		var h types.Hash
		h[0] = byte(i >> 16)
		h[1] = byte(i >> 8)
		h[2] = byte(i)
		h[31] = 1 // keep it non-zero so it isn't treated as the sentinel
		if _, err := dw.InternCodeHash(h); err != nil {
			gotErr = err
			break
		}
	}
	require.Error(t, gotErr, "expected MDBX_MAP_FULL before exhausting the loop")
	require.Contains(t, gotErr.Error(), "Put")
}

// TestBufferedDictWriter_Flush_MapFull drives Flush's Put error-wrapping
// branches: a large pending set (built in memory, no MDBX writes yet) is
// flushed into an undersized map so the Put loop fails partway through.
func TestBufferedDictWriter_Flush_MapFull(t *testing.T) {
	db := newTinyMapTestDB(t)
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	bw := NewBufferedDictWriter(roTx)
	for i := 0; i < 200000; i++ {
		var a types.Address
		a[0] = byte(i >> 16)
		a[1] = byte(i >> 8)
		a[2] = byte(i)
		_, err := bw.InternAddr(a)
		require.NoError(t, err)
	}

	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer rwTx.Rollback()

	err = bw.Flush(rwTx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Put")
}

// TestFlushPending_MapFull drives FlushPending's Put error-wrapping
// branches the same way, via a hand-built oversized DictPending snapshot.
func TestFlushPending_MapFull(t *testing.T) {
	db := newTinyMapTestDB(t)

	addrs := make(map[types.Address]uint32, 200000)
	for i := 0; i < 200000; i++ {
		var a types.Address
		a[0] = byte(i >> 16)
		a[1] = byte(i >> 8)
		a[2] = byte(i)
		addrs[a] = uint32(i + 1)
	}
	pending := &DictPending{
		Addrs:       addrs,
		HasCounters: true,
		AddrNext:    uint32(len(addrs) + 1),
	}

	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer rwTx.Rollback()

	err = FlushPending(rwTx, pending)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Put")
}
