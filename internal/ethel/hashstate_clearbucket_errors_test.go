// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hashstate_clearbucket_errors_test.go covers the early ClearBucket error
// branches shared by CalcStateRoot, RebuildHashedState, BootstrapHPH,
// FullStateRootVerify, RebuildHashedStateETL, and BootstrapHPHBatched's
// phase-1 clear. Each wraps a real kv.RwTx/kv.RwDB (built the same way
// hashstate_error_test.go's seekErrorTx/seekErrorDB wrap a real Tx) so the
// rest of MDBX behaves normally — only ClearBucket(failTable) is made to
// fail, which is a realistic condition (e.g. a corrupted/missing table) and
// doesn't require touching live MDBX cursor internals.

package ethel

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

type clearBucketErrorTx struct {
	kv.RwTx
	failTable string
	err       error
}

func (tx *clearBucketErrorTx) ClearBucket(table string) error {
	if table == tx.failTable {
		return tx.err
	}
	return tx.RwTx.ClearBucket(table)
}

type clearBucketErrorDB struct {
	kv.RwDB
	failTable string
	err       error
}

func (db *clearBucketErrorDB) BeginRw(ctx context.Context) (kv.RwTx, error) {
	tx, err := db.RwDB.BeginRw(ctx)
	if err != nil {
		return nil, err
	}
	return &clearBucketErrorTx{RwTx: tx, failTable: db.failTable, err: db.err}, nil
}

var errClearBucket = errors.New("injected clear-bucket failure")

func TestCalcStateRoot_ClearBucketError(t *testing.T) {
	for _, tbl := range []string{kv.TrieOfAccounts, kv.TrieOfStorage} {
		tbl := tbl
		t.Run(tbl, func(t *testing.T) {
			db := memdb.NewTestDB(t)
			tx, err := db.BeginRw(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			wrapped := &clearBucketErrorTx{RwTx: tx, failTable: tbl, err: errClearBucket}

			_, err = CalcStateRoot(wrapped)
			if !errors.Is(err, errClearBucket) {
				t.Fatalf("CalcStateRoot error = %v, want %v", err, errClearBucket)
			}
		})
	}
}

func TestRebuildHashedState_ClearBucketError(t *testing.T) {
	for _, tbl := range []string{kv.HashedAccounts, kv.HashedStorage} {
		tbl := tbl
		t.Run(tbl, func(t *testing.T) {
			db := memdb.NewTestDB(t)
			tx, err := db.BeginRw(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			wrapped := &clearBucketErrorTx{RwTx: tx, failTable: tbl, err: errClearBucket}

			err = RebuildHashedState(wrapped)
			if !errors.Is(err, errClearBucket) {
				t.Fatalf("RebuildHashedState error = %v, want %v", err, errClearBucket)
			}
		})
	}
}

func TestBootstrapHPH_ClearBucketError(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wrapped := &clearBucketErrorTx{RwTx: tx, failTable: kv.TrieOfStorage, err: errClearBucket}

	_, err = BootstrapHPH(wrapped)
	if !errors.Is(err, errClearBucket) {
		t.Fatalf("BootstrapHPH error = %v, want %v", err, errClearBucket)
	}
}

func TestFullStateRootVerify_ClearBucketError(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wrapped := &clearBucketErrorTx{RwTx: tx, failTable: kv.TrieOfAccounts, err: errClearBucket}

	_, err = FullStateRootVerify(nil, wrapped, 1)
	if !errors.Is(err, errClearBucket) {
		t.Fatalf("FullStateRootVerify error = %v, want %v", err, errClearBucket)
	}
}

func TestRebuildHashedStateETL_ClearBucketError(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wrapped := &clearBucketErrorTx{RwTx: tx, failTable: kv.HashedStorage, err: errClearBucket}

	err = RebuildHashedStateETL(context.Background(), wrapped, t.TempDir(), log2.New())
	if !errors.Is(err, errClearBucket) {
		t.Fatalf("RebuildHashedStateETL error = %v, want %v", err, errClearBucket)
	}
}

func TestBootstrapHPHBatched_ClearBucketError(t *testing.T) {
	db := memdb.NewTestDB(t)
	wrapped := &clearBucketErrorDB{RwDB: db, failTable: kv.TrieOfAccounts, err: errClearBucket}

	_, err := BootstrapHPHBatched(context.Background(), wrapped, 0, 0)
	if !errors.Is(err, errClearBucket) {
		t.Fatalf("BootstrapHPHBatched error = %v, want %v", err, errClearBucket)
	}
}

func TestBootstrapHPHFastETL_ClearBucketError(t *testing.T) {
	db := memdb.NewTestDB(t)
	wrapped := &clearBucketErrorDB{RwDB: db, failTable: kv.HashedAccounts, err: errClearBucket}

	_, err := BootstrapHPHFastETL(context.Background(), wrapped, t.TempDir())
	if !errors.Is(err, errClearBucket) {
		t.Fatalf("BootstrapHPHFastETL error = %v, want %v", err, errClearBucket)
	}
}
