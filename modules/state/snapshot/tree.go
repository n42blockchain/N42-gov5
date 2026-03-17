// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshot

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// DefaultMaxDiffLayers is the default cap on stacked diff layers before
// the tree starts flattening the oldest ones into the disk layer's cache.
const DefaultMaxDiffLayers = 128

var (
	ErrParentNotFound = errors.New("snapshot: parent layer not found")
	ErrLayerExists    = errors.New("snapshot: layer already exists for root")
	ErrSnapshotStale  = errors.New("snapshot: layer is stale")
)

// Tree manages the snapshot acceleration layer hierarchy.
// It maintains a mapping from block root hash to Layer, with a single
// DiskLayer at the bottom and zero or more DiffLayers stacked on top.
type Tree struct {
	layers        map[types.Hash]Layer // root -> layer
	diskLayer     *DiskLayer
	db            kv.RwDB // nil if persistence is disabled
	lock          sync.RWMutex
	maxDiffLayers int
}

// NewTree creates a new snapshot tree with the given disk layer cache.
func NewTree(cache *layered.ShardedCache, headBlock uint64, headRoot types.Hash, maxDiffLayers int) *Tree {
	if maxDiffLayers <= 0 {
		maxDiffLayers = DefaultMaxDiffLayers
	}
	disk := NewDiskLayer(cache, headBlock, headRoot)
	t := &Tree{
		layers:        make(map[types.Hash]Layer),
		diskLayer:     disk,
		maxDiffLayers: maxDiffLayers,
	}
	t.layers[headRoot] = disk
	return t
}

// SetDB attaches a database for persisted snapshot operations.
// This enables flatten-to-disk and disk layer reads.
func (t *Tree) SetDB(db kv.RwDB) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.db = db
	if t.diskLayer != nil {
		t.diskLayer.SetDB(db)
	}
}

// SetGenReady marks the flat snapshot as fully generated, enabling disk reads.
func (t *Tree) SetGenReady(ready bool) {
	t.lock.Lock()
	defer t.lock.Unlock()
	if t.diskLayer != nil {
		t.diskLayer.SetGenReady(ready)
	}
}

// Snapshot returns the layer associated with the given root hash, or nil.
func (t *Tree) Snapshot(root types.Hash) Layer {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.layers[root]
}

// Update adds a new diff layer for a block on top of its parent.
func (t *Tree) Update(
	blockNum uint64,
	root types.Hash,
	parentRoot types.Hash,
	accounts map[types.Address]*account.StateAccount,
	accountDels map[types.Address]struct{},
	storage map[types.Address]map[types.Hash][]byte,
) error {
	t.lock.Lock()
	defer t.lock.Unlock()

	if _, exists := t.layers[root]; exists {
		return ErrLayerExists
	}

	parent, ok := t.layers[parentRoot]
	if !ok {
		return ErrParentNotFound
	}

	diff := NewDiffLayer(parent, blockNum, root, accounts, accountDels, storage)
	t.layers[root] = diff

	diffCount, diffMem := t.sizeLocked()
	snapshotDiffLayers.Set(uint64(diffCount))
	snapshotDiffMemory.Set(diffMem)

	if diffCount > t.maxDiffLayers {
		t.flattenOldest()
	}

	return nil
}

// Cap flattens the tree to at most `layers` diff layers.
func (t *Tree) Cap(layers int) error {
	t.lock.Lock()
	defer t.lock.Unlock()

	if layers < 0 {
		layers = 0
	}

	for {
		diffCount, _ := t.sizeLocked()
		if diffCount <= layers {
			break
		}
		t.flattenOldest()
	}
	return nil
}

// flattenOldest finds the oldest diff layer (whose parent is the disk layer)
// and merges it into the disk layer's cache and optionally to disk.
func (t *Tree) flattenOldest() {
	var oldest *DiffLayer
	for _, layer := range t.layers {
		if dl, ok := layer.(*DiffLayer); ok {
			if dl.Parent() == t.diskLayer {
				oldest = dl
				break
			}
		}
	}
	if oldest == nil {
		return
	}

	// Merge into ShardedCache.
	t.mergeDiffIntoCache(oldest)

	// Persist to MDBX flat snapshot tables if DB is available.
	if t.db != nil {
		t.persistDiffToDisk(oldest)
	}

	// Create a new disk layer at the flattened block.
	newDisk := NewDiskLayer(t.diskLayer.cache, oldest.block, oldest.root)
	newDisk.SetDB(t.db)
	newDisk.SetGenReady(t.diskLayer.GenReady())

	// Re-parent children — must hold each DiffLayer's lock to avoid
	// races with concurrent Account()/Storage() reads on dl.parent.
	for _, layer := range t.layers {
		if dl, ok := layer.(*DiffLayer); ok {
			dl.lock.Lock()
			if dl.parent == oldest {
				dl.parent = newDisk
			}
			dl.lock.Unlock()
		}
	}

	// Remove old layers.
	delete(t.layers, t.diskLayer.root)
	t.diskLayer.MarkStale()
	oldest.MarkStale()
	delete(t.layers, oldest.root)

	// Install new disk layer.
	t.diskLayer = newDisk
	t.layers[newDisk.root] = newDisk

	snapshotFlattenCount.Inc()
}

// mergeDiffIntoCache writes the diff layer's data into the ShardedCache.
func (t *Tree) mergeDiffIntoCache(dl *DiffLayer) {
	cache := t.diskLayer.cache
	if cache == nil {
		return
	}

	for addr := range dl.accountDels {
		cache.Put(modules.Account, addr.Bytes(), nil)
	}

	for addr, acc := range dl.accounts {
		if acc == nil {
			continue
		}
		pb := acc.ToProtoMessage()
		enc, err := proto.Marshal(pb)
		if err != nil {
			log.Warn("Snapshot: marshal account for cache failed", "addr", addr.Hex(), "err", err)
			continue
		}
		cache.Put(modules.Account, addr.Bytes(), enc)
	}

	for addr, slots := range dl.storage {
		for key, val := range slots {
			compositeKey := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), 0, key.Bytes())
			cache.Put(modules.Storage, compositeKey, val)
		}
	}
}

// persistDiffToDisk writes the diff layer's data to the flat snapshot MDBX tables.
// This is a single atomic MDBX transaction.
func (t *Tree) persistDiffToDisk(dl *DiffLayer) {
	if t.db == nil {
		return
	}

	err := t.db.Update(context.Background(), func(tx kv.RwTx) error {
		// Deleted accounts: remove from SnapshotAccount + SnapshotStorage.
		for addr := range dl.accountDels {
			if err := rawdb.DeleteSnapshotAccount(tx, addr); err != nil {
				return err
			}
			if err := rawdb.DeleteSnapshotStorageByAddress(tx, addr); err != nil {
				return err
			}
		}

		// Modified accounts: write to SnapshotAccount.
		for addr, acc := range dl.accounts {
			if acc == nil {
				continue
			}
			pb := acc.ToProtoMessage()
			enc, err := proto.Marshal(pb)
			if err != nil {
				return fmt.Errorf("snapshot: marshal account %s: %w", addr.Hex(), err)
			}
			if err := rawdb.WriteSnapshotAccount(tx, addr, enc); err != nil {
				return err
			}
		}

		// Storage changes: write to SnapshotStorage.
		for addr, slots := range dl.storage {
			for key, val := range slots {
				compositeKey := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), 0, key.Bytes())
				if val == nil {
					if err := rawdb.DeleteSnapshotStorage(tx, compositeKey); err != nil {
						return err
					}
				} else {
					if err := rawdb.WriteSnapshotStorage(tx, compositeKey, val); err != nil {
						return err
					}
				}
			}
		}

		// Update disk root.
		return rawdb.WriteSnapshotDiskRoot(tx, dl.root, dl.block)
	})

	if err != nil {
		log.Error("Snapshot: persist diff to disk failed", "block", dl.block, "root", dl.root.Hex(), "err", err)
		snapshotPersistErrors.Inc()
	} else {
		snapshotPersistFlushCount.Inc()
	}
}

// Discard marks a layer and all dependent children as stale.
func (t *Tree) Discard(root types.Hash) {
	t.lock.Lock()
	defer t.lock.Unlock()

	if _, ok := t.layers[root]; !ok {
		return
	}

	toDiscard := []types.Hash{root}
	toDiscard = t.collectChildren(root, toDiscard)

	for _, r := range toDiscard {
		if l, ok := t.layers[r]; ok {
			if dl, ok := l.(*DiffLayer); ok {
				dl.MarkStale()
			}
			delete(t.layers, r)
		}
	}
	diffCount, diffMem := t.sizeLocked()
	snapshotDiffLayers.Set(uint64(diffCount))
	snapshotDiffMemory.Set(diffMem)
}

// collectChildren recursively finds all layers whose parent root matches.
func (t *Tree) collectChildren(parentRoot types.Hash, result []types.Hash) []types.Hash {
	for root, layer := range t.layers {
		if layer.Parent() != nil && layer.Parent().Root() == parentRoot && root != parentRoot {
			result = append(result, root)
			result = t.collectChildren(root, result)
		}
	}
	return result
}

// Size returns the number of diff layers and their total memory usage.
func (t *Tree) Size() (diffLayers int, diffMemory uint64) {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.sizeLocked()
}

func (t *Tree) sizeLocked() (int, uint64) {
	var count int
	var mem uint64
	for _, layer := range t.layers {
		if dl, ok := layer.(*DiffLayer); ok {
			count++
			mem += dl.Memory()
		}
	}
	return count, mem
}

// DiskLayer returns the current disk layer.
func (t *Tree) DiskLayer() *DiskLayer {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.diskLayer
}

// SaveJournal persists diff layers for crash recovery.
// Call during graceful shutdown.
func (t *Tree) SaveJournal() error {
	if t.db == nil {
		return nil
	}
	t.lock.RLock()
	defer t.lock.RUnlock()

	return t.db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveJournal(tx, t)
	})
}
