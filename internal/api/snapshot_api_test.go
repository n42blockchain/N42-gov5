// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package api

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func snapshotTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	return memdb.NewTestDB(t)
}

func seedSnapshot(t *testing.T, db kv.RwDB, blockNum uint64, accounts, storage, code uint64) {
	t.Helper()
	m := &snapshot.Meta{
		BlockNumber:  blockNum,
		CreatedAt:    time.Now().Unix(),
		AccountCount: accounts,
		StorageCount: storage,
		CodeCount:    code,
	}
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return snapshot.SaveSnapshot(tx, m)
	})
	if err != nil {
		t.Fatalf("seedSnapshot(%d): %v", blockNum, err)
	}
}

// TestGetSnapshotInfo_Empty verifies that GetSnapshotInfo returns zero count
// and nil latest when no snapshots have been created.
func TestGetSnapshotInfo_Empty(t *testing.T) {
	db := snapshotTestDB(t)
	api := NewSnapshotAPI(db)

	result, err := api.GetSnapshotInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Latest != nil {
		t.Error("expected nil Latest when no snapshots exist")
	}
	if result.Count != 0 {
		t.Errorf("expected count=0, got %d", result.Count)
	}
}

// TestGetSnapshotInfo_WithData verifies that GetSnapshotInfo returns the
// correct latest snapshot and total count.
func TestGetSnapshotInfo_WithData(t *testing.T) {
	db := snapshotTestDB(t)
	seedSnapshot(t, db, 10000, 500, 2000, 10)
	seedSnapshot(t, db, 20000, 600, 3000, 15)

	api := NewSnapshotAPI(db)

	result, err := api.GetSnapshotInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("expected count=2, got %d", result.Count)
	}
	if result.Latest == nil {
		t.Fatal("expected non-nil Latest")
	}
	if uint64(result.Latest.BlockNumber) != 20000 {
		t.Errorf("expected latest blockNumber=20000, got %d", result.Latest.BlockNumber)
	}
	if uint64(result.Latest.AccountCount) != 600 {
		t.Errorf("expected accountCount=600, got %d", result.Latest.AccountCount)
	}
	if uint64(result.Latest.StorageCount) != 3000 {
		t.Errorf("expected storageCount=3000, got %d", result.Latest.StorageCount)
	}
	if uint64(result.Latest.CodeCount) != 15 {
		t.Errorf("expected codeCount=15, got %d", result.Latest.CodeCount)
	}
	if result.Latest.CreatedAt == "" {
		t.Error("expected non-empty createdAt")
	}
}

// TestGetAccountRange_InvalidLimit verifies that a non-positive limit is rejected.
func TestGetAccountRange_InvalidLimit(t *testing.T) {
	db := snapshotTestDB(t)
	api := NewSnapshotAPI(db)

	_, err := api.GetAccountRange(context.Background(), hexutil.Uint64(100), types.Address{}, 0)
	if err == nil {
		t.Fatal("expected error for limit=0")
	}

	_, err = api.GetAccountRange(context.Background(), hexutil.Uint64(100), types.Address{}, -5)
	if err == nil {
		t.Fatal("expected error for negative limit")
	}
}

// TestGetStorageRange_InvalidLimit verifies that a non-positive limit is rejected.
func TestGetStorageRange_InvalidLimit(t *testing.T) {
	db := snapshotTestDB(t)
	api := NewSnapshotAPI(db)

	_, err := api.GetStorageRange(context.Background(), hexutil.Uint64(100), types.Address{}, types.Hash{}, 0)
	if err == nil {
		t.Fatal("expected error for limit=0")
	}
}

// TestGetAccountRange_NoSnapshot verifies that querying an account range at a
// non-existent snapshot height returns an error.
func TestGetAccountRange_NoSnapshot(t *testing.T) {
	db := snapshotTestDB(t)
	api := NewSnapshotAPI(db)

	_, err := api.GetAccountRange(context.Background(), hexutil.Uint64(99999), types.Address{}, 10)
	if err == nil {
		t.Fatal("expected error for non-existent snapshot")
	}
}

// TestGetStorageRange_NoSnapshot verifies that querying storage at a
// non-existent snapshot height returns an error.
func TestGetStorageRange_NoSnapshot(t *testing.T) {
	db := snapshotTestDB(t)
	api := NewSnapshotAPI(db)

	_, err := api.GetStorageRange(context.Background(), hexutil.Uint64(99999), types.Address{}, types.Hash{}, 10)
	if err == nil {
		t.Fatal("expected error for non-existent snapshot")
	}
}

// TestMetaToResult verifies the conversion from snapshot.Meta to SnapshotMeta.
func TestMetaToResult(t *testing.T) {
	ts := int64(1700000000)
	m := &snapshot.Meta{
		BlockNumber:  42000,
		CreatedAt:    ts,
		AccountCount: 100,
		StorageCount: 200,
		CodeCount:    5,
	}
	r := snapshotMetaFromInternal(m)
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if uint64(r.BlockNumber) != 42000 {
		t.Errorf("blockNumber mismatch: got %d", r.BlockNumber)
	}
	expected := time.Unix(ts, 0).UTC().Format(time.RFC3339)
	if r.CreatedAt != expected {
		t.Errorf("createdAt mismatch: got %s, want %s", r.CreatedAt, expected)
	}

	// nil input
	if snapshotMetaFromInternal(nil) != nil {
		t.Error("expected nil for nil input")
	}
}
