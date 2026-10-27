// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// chainedFlushRows buffers a flush for a later transaction, as the lag
// writer's qmdbFlushRows does.
type chainedFlushRows struct {
	rows []chainedFlushRow
}

type chainedFlushRow struct {
	table      string
	key, value []byte
	del        bool
}

func (f *chainedFlushRows) Put(table string, key, value []byte) error {
	f.rows = append(f.rows, chainedFlushRow{table: table, key: key, value: value})
	return nil
}

func (f *chainedFlushRows) Delete(table string, key []byte) error {
	f.rows = append(f.rows, chainedFlushRow{table: table, key: key, del: true})
	return nil
}

func (f *chainedFlushRows) replay(tx kv.RwTx) error {
	for _, r := range f.rows {
		var err error
		if r.del {
			err = tx.Delete(r.table, r.key)
		} else {
			err = tx.Put(r.table, r.key, r.value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func tableRows(t *testing.T, db kv.RwDB, table string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		return tx.ForEach(table, nil, func(k, v []byte) error {
			out[string(k)] = append([]byte(nil), v...)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestQMDBChainedFlushMatchesSerial: three blocks flushed while the earlier
// ones are still staged (FlushToChained, the N42_LAG_WRITE_QUEUE writer),
// their rows replayed in block order and the cursors committed oldest
// first, leave the store a serial flush-commit-evict per block leaves: the
// same roots, the same twig meta, leaf blobs and meta rows, every serial
// entry row, the same bytes written per block, and a reload that continues
// to the same root. The blocks span several twigs and overwrite keys of
// still-staged blocks.
func TestQMDBChainedFlushMatchesSerial(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })

	mk := func(nonce uint64, ranges ...[2]int) map[types.Address]*account.StateAccount {
		accts := map[types.Address]*account.StateAccount{}
		for _, r := range ranges {
			for i := r[0]; i < r[1]; i++ {
				accts[rlAddr(i)] = qmAcct(nonce, uint64(1000+i)+nonce)
			}
		}
		return accts
	}
	blocks := []map[types.Address]*account.StateAccount{
		mk(1, [2]int{0, 3000}),
		mk(2, [2]int{3000, 8000}, [2]int{0, 1000}),
		mk(3, [2]int{8000, 11000}, [2]int{2500, 3500}),
		mk(4, [2]int{11000, 13000}, [2]int{5000, 6000}, [2]int{9000, 9500}),
	}
	next := mk(5, [2]int{13000, 14000}, [2]int{100, 200}, [2]int{10000, 10200})

	// Serial: flush, commit, adopt, evict per block.
	serDB := memdb.NewTestDB(t)
	ser := NewQMDBRootComputer()
	ser.EnableUndoRecording()
	var serRoots []types.Hash
	var serBytes []int
	for _, b := range blocks {
		tx, err := serDB.BeginRw(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ser.SetCold(tx)
		root, err := ser.ComputeRoot(b, nil)
		if err != nil {
			t.Fatal(err)
		}
		n, err := ser.FlushTo(tx)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		ser.CommitFlushed()
		ser.EvictFlushed()
		ser.SetCold(nil)
		ser.TakeUndo()
		serRoots = append(serRoots, root)
		serBytes = append(serBytes, n)
	}

	// Chained: block 1 committed, blocks 2..4 staged one behind another.
	chDB := memdb.NewTestDB(t)
	ch := NewQMDBRootComputer()
	ch.EnableUndoRecording()
	rlApplyBlock(t, chDB, ch, blocks[0])
	rtx, err := chDB.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rtxOpen := true
	defer func() {
		if rtxOpen {
			ch.SetCold(nil)
			rtx.Rollback()
		}
	}()
	ch.SetCold(rtx)
	var bufs []*chainedFlushRows
	for i, b := range blocks[1:] {
		root, err := ch.ComputeRoot(b, nil)
		if err != nil {
			t.Fatal(err)
		}
		if root != serRoots[i+1] {
			t.Fatalf("block %d: chained root %x, serial %x", i+2, root[:8], serRoots[i+1][:8])
		}
		buf := &chainedFlushRows{}
		n, err := ch.FlushToChained(buf)
		if err != nil {
			t.Fatal(err)
		}
		if n != serBytes[i+1] {
			t.Fatalf("block %d: chained flush wrote %d bytes, serial %d", i+2, n, serBytes[i+1])
		}
		if got := ch.StagedFlushes(); got != i+1 {
			t.Fatalf("block %d: %d flushes staged, want %d", i+2, got, i+1)
		}
		ch.TakeUndo()
		bufs = append(bufs, buf)
	}
	ch.SetCold(nil)
	rtx.Rollback()
	rtxOpen = false
	for i, buf := range bufs {
		if err := chDB.Update(t.Context(), buf.replay); err != nil {
			t.Fatal(err)
		}
		ch.CommitFlushed()
		ch.EvictFlushed()
		if got, want := ch.StagedFlushes(), len(bufs)-1-i; got != want {
			t.Fatalf("after commit %d: %d flushes staged, want %d", i+2, got, want)
		}
	}
	if ch.FlushedThrough() != ser.FlushedThrough() {
		t.Fatalf("flushed cursor %d, serial %d", ch.FlushedThrough(), ser.FlushedThrough())
	}

	for _, table := range []string{qmdb.TwigTable, qmdb.LeavesTable, qmdb.MetaTable} {
		a, b := tableRows(t, serDB, table), tableRows(t, chDB, table)
		if len(a) != len(b) {
			t.Fatalf("%s: %d rows serial, %d chained", table, len(a), len(b))
		}
		for k, v := range a {
			if !bytes.Equal(v, b[k]) {
				t.Fatalf("%s: row %x differs", table, k)
			}
		}
	}
	// Entry rows: every serial row is there; the chained store may keep a
	// dead row of a block that was still staged when its key was
	// overwritten (unreclaimed garbage, never read).
	sa, sb := tableRows(t, serDB, qmdb.EntryTable), tableRows(t, chDB, qmdb.EntryTable)
	for k, v := range sa {
		if !bytes.Equal(v, sb[k]) {
			t.Fatalf("entry row %x missing or different in the chained store", k)
		}
	}

	// Both go on to the same root, live and from a reload.
	_, serNext := rlApplyBlock(t, serDB, ser, next)
	_, chNext := rlApplyBlock(t, chDB, ch, next)
	if serNext != chNext {
		t.Fatalf("next block: chained root %x, serial %x", chNext[:8], serNext[:8])
	}
	fresh := NewQMDBRootComputer()
	if err := chDB.Update(t.Context(), func(tx kv.RwTx) error {
		fresh.SetCold(tx)
		defer fresh.SetCold(nil)
		return fresh.LoadFrom(tx)
	}); err != nil {
		t.Fatal(err)
	}
	if fresh.Root() != serNext {
		t.Fatalf("reloaded chained store root %x, want %x", fresh.Root().Bytes()[:8], serNext[:8])
	}
}

// TestQMDBChainedFlushWithoutStagedIsFlushTo: with nothing staged,
// FlushToChained is FlushTo.
func TestQMDBChainedFlushWithoutStagedIsFlushTo(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	accts := map[types.Address]*account.StateAccount{}
	for i := 0; i < 100; i++ {
		accts[rlAddr(i)] = qmAcct(1, uint64(i))
	}
	rc := NewQMDBRootComputer()
	if _, err := rc.ComputeRoot(accts, nil); err != nil {
		t.Fatal(err)
	}
	a := &chainedFlushRows{}
	if _, err := rc.FlushToChained(a); err != nil {
		t.Fatal(err)
	}
	if rc.StagedFlushes() != 1 {
		t.Fatalf("%d flushes staged, want 1", rc.StagedFlushes())
	}
	rc.AbortFlushed()
	b := &chainedFlushRows{}
	if _, err := rc.FlushTo(b); err != nil {
		t.Fatal(err)
	}
	if len(a.rows) != len(b.rows) {
		t.Fatalf("%d rows chained, %d FlushTo", len(a.rows), len(b.rows))
	}
	for i := range a.rows {
		if a.rows[i].table != b.rows[i].table || !bytes.Equal(a.rows[i].key, b.rows[i].key) || !bytes.Equal(a.rows[i].value, b.rows[i].value) {
			t.Fatalf("row %d differs", i)
		}
	}
}
