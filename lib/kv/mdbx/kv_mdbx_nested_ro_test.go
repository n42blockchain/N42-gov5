// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mdbx

import (
	"context"
	"testing"

	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// TestBeginRoNestedTakesNoPermit checks that a nested read transaction opens
// while the only limiter permit is held, and that closing it does not release
// a permit it never took.
func TestBeginRoNestedTakesNoPermit(t *testing.T) {
	sem := semaphore.NewWeighted(1)
	db := NewMDBX(log.New()).InMem(t.TempDir()).RoTxsLimiter(sem).MustOpen()
	defer db.Close()

	outer, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sem.TryAcquire(1) {
		t.Fatal("outer read tx did not take the permit")
	}
	inner, err := kv.BeginRoNested(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	inner.Rollback()
	if sem.TryAcquire(1) {
		t.Fatal("nested rollback released the outer transaction's permit")
	}
	outer.Rollback()
	if !sem.TryAcquire(1) {
		t.Fatal("outer rollback did not release its permit")
	}
	sem.Release(1)
}

// TestAcquireRoPermit checks that a permit taken without a transaction blocks
// BeginRo on a one-permit limiter, covers a nested transaction, and releases
// exactly once however often its release is called.
func TestAcquireRoPermit(t *testing.T) {
	sem := semaphore.NewWeighted(1)
	db := NewMDBX(log.New()).InMem(t.TempDir()).RoTxsLimiter(sem).MustOpen()
	defer db.Close()

	release, err := kv.AcquireRoPermit(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if sem.TryAcquire(1) {
		t.Fatal("permit not taken")
	}
	inner, err := kv.BeginRoNested(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	inner.Rollback()
	release()
	release()
	if !sem.TryAcquire(1) {
		t.Fatal("release did not return the permit")
	}
	if sem.TryAcquire(1) {
		t.Fatal("a second release returned a permit twice")
	}
	sem.Release(1)
}
