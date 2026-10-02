// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers qmdbMDBXIndex, the MDBX-backed qmdb.Index implementation left at 0%:
// Get/Put/Delete/Len bookkeeping, persistCount + restore across a fresh
// newQMDBMDBXIndex, and setTx repointing at a new batch's RwTx.

package commitment

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

func qiHash(b byte) qmdb.Hash {
	var h qmdb.Hash
	h[31] = b
	return h
}

func TestQMDBMDBXIndexBasic(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init() // register the N42 table list (qmdbIndex/qmdbMeta included)
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	idx := newQMDBMDBXIndex(tx)
	if idx.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 on a fresh index", idx.Len())
	}

	if _, ok := idx.Get(qiHash(1)); ok {
		t.Fatal("Get on an absent key must report !ok")
	}

	idx.Put(qiHash(1), 100)
	idx.Put(qiHash(2), 200)
	if idx.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", idx.Len())
	}
	if v, ok := idx.Get(qiHash(1)); !ok || v != 100 {
		t.Fatalf("Get(1) = (%d,%v), want (100,true)", v, ok)
	}

	// Put on an existing key updates the slot without growing the count.
	idx.Put(qiHash(1), 999)
	if idx.Len() != 2 {
		t.Fatalf("Len() after overwrite = %d, want 2", idx.Len())
	}
	if v, ok := idx.Get(qiHash(1)); !ok || v != 999 {
		t.Fatalf("Get(1) after overwrite = (%d,%v), want (999,true)", v, ok)
	}

	// Delete on an absent key is a no-op.
	idx.Delete(qiHash(77))
	if idx.Len() != 2 {
		t.Fatalf("Len() after no-op delete = %d, want 2", idx.Len())
	}

	idx.Delete(qiHash(2))
	if idx.Len() != 1 {
		t.Fatalf("Len() after delete = %d, want 1", idx.Len())
	}
	if _, ok := idx.Get(qiHash(2)); ok {
		t.Fatal("Get after Delete must report !ok")
	}

	idx.persistCount()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// A fresh index wrapping a new tx over the same DB must restore the
	// persisted count.
	tx2, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	idx2 := newQMDBMDBXIndex(tx2)
	if idx2.Len() != 1 {
		t.Fatalf("restored Len() = %d, want 1", idx2.Len())
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}

	// setTx repoints at a new batch's tx without resetting the in-memory count.
	tx3, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx3.Rollback()
	idx2.setTx(tx3)
	idx2.Put(qiHash(3), 300)
	if v, ok := idx2.Get(qiHash(3)); !ok || v != 300 {
		t.Fatalf("Get(3) after setTx = (%d,%v), want (300,true)", v, ok)
	}
}
