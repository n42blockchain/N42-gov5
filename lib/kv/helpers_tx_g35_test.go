/*
   External-package tests for the kv.RoDB/kv.Tx-backed helpers (FirstKey,
   LastKey, BigChunks, ReadAhead). These need a real (in-memory) MDBX
   environment, which lives in lib/kv/memdb and itself imports lib/kv, so
   this file is in package kv_test to avoid an import cycle.
*/

package kv_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestG35FirstKeyLastKeyEmpty(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	k, err := kv.FirstKey(tx, modules.AccountChangeSet)
	if err != nil {
		t.Fatalf("FirstKey: %v", err)
	}
	if k != nil {
		t.Fatalf("expected nil first key on empty bucket, got %x", k)
	}

	k, err = kv.LastKey(tx, modules.AccountChangeSet)
	if err != nil {
		t.Fatalf("LastKey: %v", err)
	}
	if k != nil {
		t.Fatalf("expected nil last key on empty bucket, got %x", k)
	}
}

func TestG35FirstKeyLastKeyPopulated(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	if err := tx.Put(modules.AccountChangeSet, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}, []byte("a")); err != nil {
		t.Fatalf("put 1: %v", err)
	}
	if err := tx.Put(modules.AccountChangeSet, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x09}, []byte("b")); err != nil {
		t.Fatalf("put 9: %v", err)
	}

	first, err := kv.FirstKey(tx, modules.AccountChangeSet)
	if err != nil {
		t.Fatalf("FirstKey: %v", err)
	}
	if first[7] != 0x01 {
		t.Fatalf("expected first key to end in 0x01, got %x", first)
	}

	last, err := kv.LastKey(tx, modules.AccountChangeSet)
	if err != nil {
		t.Fatalf("LastKey: %v", err)
	}
	if last[7] != 0x09 {
		t.Fatalf("expected last key to end in 0x09, got %x", last)
	}
}

func TestG35BigChunksWalksAllEntries(t *testing.T) {
	db, tx := memdb.NewTestTx(t)

	for i := byte(1); i <= 5; i++ {
		key := []byte{0, 0, 0, 0, 0, 0, 0, i}
		if err := tx.Put(modules.AccountChangeSet, key, []byte{i}); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var count int
	err := kv.BigChunks(db, modules.AccountChangeSet, nil, func(tx kv.Tx, k, v []byte) (bool, error) {
		count++
		return true, nil
	})
	if err != nil {
		t.Fatalf("BigChunks: %v", err)
	}
	if count != 5 {
		t.Fatalf("expected to walk 5 entries, got %d", count)
	}
}

func TestG35BigChunksStopsEarly(t *testing.T) {
	db, tx := memdb.NewTestTx(t)
	for i := byte(1); i <= 3; i++ {
		key := []byte{0, 0, 0, 0, 0, 0, 0, i}
		if err := tx.Put(modules.AccountChangeSet, key, []byte{i}); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var count int
	err := kv.BigChunks(db, modules.AccountChangeSet, nil, func(tx kv.Tx, k, v []byte) (bool, error) {
		count++
		return false, nil // stop after first entry
	})
	if err != nil {
		t.Fatalf("BigChunks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected walker to run exactly once before stop, got %d", count)
	}
}

func TestG35ReadAhead(t *testing.T) {
	db, tx := memdb.NewTestTx(t)
	for i := byte(1); i <= 3; i++ {
		key := []byte{0, 0, 0, 0, 0, 0, 0, i}
		if err := tx.Put(modules.AccountChangeSet, key, []byte{i}); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var inProgress atomic.Bool
	clean := kv.ReadAhead(context.Background(), db, &inProgress, modules.AccountChangeSet, nil, 10)
	clean() // waits for the readahead goroutine to finish

	// A nil db returns the no-op cleaner immediately.
	var inProgress2 atomic.Bool
	clean2 := kv.ReadAhead(context.Background(), nil, &inProgress2, modules.AccountChangeSet, nil, 10)
	clean2()

	// A CAS that's already true should also return the no-op cleaner.
	var busy atomic.Bool
	busy.Store(true)
	clean3 := kv.ReadAhead(context.Background(), db, &busy, modules.AccountChangeSet, nil, 10)
	clean3()
}
