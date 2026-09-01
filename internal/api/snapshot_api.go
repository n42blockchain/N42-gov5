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
//
// SnapshotAPI exposes state snapshot inspection under the debug namespace.
// Backed by a kv.RwDB handle to the internal snapshot manager, methods
// such as GetSnapshotInfo return a SnapshotInfoResult containing the
// latest snapshot metadata and the total retained count. Gated on the
// snapshot manager being active in node configuration.

package api

import (
	"context"
	"errors"
	"time"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/lib/kv"
)

// SnapshotAPI provides JSON-RPC methods for querying state snapshot data.
// It is registered under the "debug" namespace and gated on the snapshot
// manager being active.
type SnapshotAPI struct {
	db kv.RwDB
}

// NewSnapshotAPI creates a new snapshot query API backed by the given database.
func NewSnapshotAPI(db kv.RwDB) *SnapshotAPI {
	return &SnapshotAPI{db: db}
}

// SnapshotInfoResult is the JSON-RPC response for GetSnapshotInfo.
type SnapshotInfoResult struct {
	// Latest is the most recent snapshot, or nil when no snapshots exist.
	Latest *SnapshotMeta `json:"latest"`
	// Count is the total number of retained snapshots.
	Count int `json:"count"`
}

// SnapshotMeta mirrors snapshot.Meta with JSON-friendly types.
type SnapshotMeta struct {
	BlockNumber  hexutil.Uint64 `json:"blockNumber"`
	CreatedAt    string         `json:"createdAt"` // RFC3339
	AccountCount hexutil.Uint64 `json:"accountCount"`
	StorageCount hexutil.Uint64 `json:"storageCount"`
	CodeCount    hexutil.Uint64 `json:"codeCount"`
}

func snapshotMetaFromInternal(m *snapshot.Meta) *SnapshotMeta {
	if m == nil {
		return nil
	}
	return &SnapshotMeta{
		BlockNumber:  hexutil.Uint64(m.BlockNumber),
		CreatedAt:    time.Unix(m.CreatedAt, 0).UTC().Format(time.RFC3339),
		AccountCount: hexutil.Uint64(m.AccountCount),
		StorageCount: hexutil.Uint64(m.StorageCount),
		CodeCount:    hexutil.Uint64(m.CodeCount),
	}
}

// GetSnapshotInfo returns metadata about the latest snapshot and the total
// number of retained snapshots. Returns an error if the snapshot subsystem
// is unavailable.
func (s *SnapshotAPI) GetSnapshotInfo(ctx context.Context) (*SnapshotInfoResult, error) {
	tx, err := s.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	latest, err := snapshot.LatestSnapshot(tx)
	if err != nil {
		return nil, err
	}

	list, err := snapshot.ListSnapshots(tx)
	if err != nil {
		return nil, err
	}

	return &SnapshotInfoResult{
		Latest: snapshotMetaFromInternal(latest),
		Count:  len(list),
	}, nil
}

// SnapAccountEntry is one account in the GetAccountRange response.
type SnapAccountEntry struct {
	Address types.Address `json:"address"`
	Data    hexutil.Bytes `json:"data"` // protobuf-encoded StateAccount
}

// SnapAccountRangeResult is the JSON-RPC response for GetAccountRange.
type SnapAccountRangeResult struct {
	Accounts []SnapAccountEntry `json:"accounts"`
	NextKey  *types.Address     `json:"nextKey"` // nil when iteration is complete
}

// GetAccountRange returns accounts in the snapshot at the given block number,
// starting from `start` (inclusive). Limit is capped server-side.
func (s *SnapshotAPI) GetAccountRange(ctx context.Context, blockNumber hexutil.Uint64, start types.Address, limit int) (*SnapAccountRangeResult, error) {
	if limit <= 0 {
		return nil, errors.New("limit must be positive")
	}

	tx, err := s.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	entries, next, err := snapshot.ReadAccountRange(tx, uint64(blockNumber), start, limit)
	if err != nil {
		return nil, err
	}

	result := &SnapAccountRangeResult{
		Accounts: make([]SnapAccountEntry, len(entries)),
		NextKey:  next,
	}
	for i, e := range entries {
		result.Accounts[i] = SnapAccountEntry{
			Address: e.Address,
			Data:    e.Data,
		}
	}
	return result, nil
}

// SnapStorageEntry is one storage slot in the GetStorageRange response.
type SnapStorageEntry struct {
	Key   hexutil.Bytes `json:"key"`   // composite key: address(20) + storageKey(32)
	Value hexutil.Bytes `json:"value"`
}

// SnapStorageRangeResult is the JSON-RPC response for GetStorageRange.
type SnapStorageRangeResult struct {
	Slots   []SnapStorageEntry `json:"slots"`
	NextKey *types.Hash        `json:"nextKey"` // nil when iteration is complete
}

// GetStorageRange returns storage slots for an account at the given snapshot
// height, starting from `start` (inclusive).
func (s *SnapshotAPI) GetStorageRange(ctx context.Context, blockNumber hexutil.Uint64, account types.Address, start types.Hash, limit int) (*SnapStorageRangeResult, error) {
	if limit <= 0 {
		return nil, errors.New("limit must be positive")
	}

	tx, err := s.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	entries, next, err := snapshot.ReadStorageRange(tx, uint64(blockNumber), account, start, limit)
	if err != nil {
		return nil, err
	}

	result := &SnapStorageRangeResult{
		Slots:   make([]SnapStorageEntry, len(entries)),
		NextKey: next,
	}
	for i, e := range entries {
		result.Slots[i] = SnapStorageEntry{
			Key:   e.Key,
			Value: e.Value,
		}
	}
	return result, nil
}
