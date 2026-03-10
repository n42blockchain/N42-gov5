// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshot

import (
	"context"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"google.golang.org/protobuf/proto"

	state_proto "github.com/n42blockchain/N42/api/protocol/state"
)

// DiskLayer is the bottom layer of the snapshot tree, backed by ShardedCache
// and optionally by persisted flat snapshot tables (SnapshotAccount/SnapshotStorage).
//
// When db is set and snapshot generation is complete, Account/Storage reads
// first check ShardedCache, then fall back to the flat snapshot tables.
// This means the SnapshotStateReader no longer needs to fall through to the
// full state reader for data covered by the snapshot.
//
// When db is nil or generation is incomplete, Account/Storage return (nil, false)
// as before, causing the SnapshotStateReader to use the inner reader.
type DiskLayer struct {
	cache     *layered.ShardedCache
	db        kv.RwDB // nil if persistence is not enabled
	block     uint64
	root      types.Hash
	stale     atomic.Bool
	genReady  atomic.Bool // true when flat snapshot is fully generated
}

// NewDiskLayer creates a new disk layer backed by the given cache.
func NewDiskLayer(cache *layered.ShardedCache, blockNum uint64, root types.Hash) *DiskLayer {
	return &DiskLayer{
		cache: cache,
		block: blockNum,
		root:  root,
	}
}

// SetDB attaches a database for persisted snapshot reads.
func (dl *DiskLayer) SetDB(db kv.RwDB) {
	dl.db = db
}

// SetGenReady marks the flat snapshot tables as fully generated and ready for reads.
func (dl *DiskLayer) SetGenReady(ready bool) {
	dl.genReady.Store(ready)
}

// Root returns the state root / block hash this layer was created at.
func (dl *DiskLayer) Root() types.Hash { return dl.root }

// BlockNumber returns the block number of this layer.
func (dl *DiskLayer) BlockNumber() uint64 { return dl.block }

// Parent returns nil — the disk layer has no parent.
func (dl *DiskLayer) Parent() Layer { return nil }

// Stale returns true if this disk layer has been superseded.
func (dl *DiskLayer) Stale() bool { return dl.stale.Load() }

// Account reads an account from the flat snapshot tables.
// Returns (account, true) if found, (nil, false) if not available.
func (dl *DiskLayer) Account(addr types.Address) (*account.StateAccount, bool) {
	if !dl.genReady.Load() || dl.db == nil {
		return nil, false
	}

	tx, err := dl.db.BeginRo(context.Background())
	if err != nil {
		return nil, false
	}
	defer tx.Rollback()

	data, err := rawdb.ReadSnapshotAccount(tx, addr)
	if err != nil || data == nil {
		return nil, false
	}

	pb := new(state_proto.Account)
	if err := proto.Unmarshal(data, pb); err != nil {
		return nil, false
	}
	acc := new(account.StateAccount)
	if err := acc.FromProtoMessage(pb); err != nil {
		return nil, false
	}

	snapshotDiskHits.Inc()
	return acc, true
}

// Storage reads a storage slot from the flat snapshot tables.
// Returns (value, true) if found, (nil, false) if not available.
func (dl *DiskLayer) Storage(addr types.Address, key types.Hash) ([]byte, bool) {
	if !dl.genReady.Load() || dl.db == nil {
		return nil, false
	}

	tx, err := dl.db.BeginRo(context.Background())
	if err != nil {
		return nil, false
	}
	defer tx.Rollback()

	compositeKey := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), 0, key.Bytes())
	data, err := rawdb.ReadSnapshotStorage(tx, compositeKey)
	if err != nil || data == nil {
		return nil, false
	}

	snapshotDiskHits.Inc()
	return data, true
}

// AccountDeleted always returns false for the disk layer.
func (dl *DiskLayer) AccountDeleted(_ types.Address) bool {
	return false
}

// MarkStale marks this disk layer as superseded.
func (dl *DiskLayer) MarkStale() {
	dl.stale.Store(true)
}

// Cache returns the underlying ShardedCache.
func (dl *DiskLayer) Cache() *layered.ShardedCache {
	return dl.cache
}

// DB returns the underlying database, or nil.
func (dl *DiskLayer) DB() kv.RwDB {
	return dl.db
}

// GenReady returns whether the flat snapshot is fully generated.
func (dl *DiskLayer) GenReady() bool {
	return dl.genReady.Load()
}

// Compile-time check.
var _ Layer = (*DiskLayer)(nil)
