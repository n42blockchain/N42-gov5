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

package snapshot

import (
	"errors"
	"sync"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/modules"
	"google.golang.org/protobuf/proto"
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
	lock          sync.RWMutex
	maxDiffLayers int
}

// NewTree creates a new snapshot tree with the given disk layer cache.
// headBlock is the current chain head block number.
// maxDiffLayers controls how many diff layers accumulate before flattening.
// If maxDiffLayers <= 0, DefaultMaxDiffLayers is used.
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

// Snapshot returns the layer associated with the given root hash, or nil.
func (t *Tree) Snapshot(root types.Hash) Layer {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.layers[root]
}

// Update adds a new diff layer for a block on top of its parent.
// After insertion, if the total diff layer count exceeds maxDiffLayers,
// the oldest diff layers are flattened into the disk cache.
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

	// Prevent duplicate insertion.
	if _, exists := t.layers[root]; exists {
		return ErrLayerExists
	}

	parent, ok := t.layers[parentRoot]
	if !ok {
		return ErrParentNotFound
	}

	diff := NewDiffLayer(parent, blockNum, root, accounts, accountDels, storage)
	t.layers[root] = diff

	// Update metrics.
	diffCount, diffMem := t.sizeLocked()
	snapshotDiffLayers.Set(uint64(diffCount))
	snapshotDiffMemory.Set(diffMem)

	// Auto-flatten if over the cap.
	if diffCount > t.maxDiffLayers {
		t.flattenOldest()
	}

	return nil
}

// Cap flattens the tree to at most `layers` diff layers, merging the rest
// into the disk layer's ShardedCache.
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
// and merges it into the disk layer's cache.
// Caller must hold t.lock.
func (t *Tree) flattenOldest() {
	// Find a diff layer whose parent is the current disk layer.
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

	// Merge the oldest diff layer into the disk cache.
	t.mergeDiffIntoCache(oldest)

	// Create a new disk layer at the flattened block.
	newDisk := NewDiskLayer(t.diskLayer.cache, oldest.block, oldest.root)

	// Re-parent all diff layers that had the oldest as parent.
	for root, layer := range t.layers {
		if dl, ok := layer.(*DiffLayer); ok {
			if dl.parent == oldest {
				dl.parent = newDisk
			}
		}
		_ = root
	}

	// Remove the old disk layer and the flattened diff layer.
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

	// Merge deleted accounts — write nil (negative cache).
	for addr := range dl.accountDels {
		cache.Put(modules.Account, addr.Bytes(), nil)
	}

	// Merge modified accounts.
	for addr, acc := range dl.accounts {
		pb := acc.ToProtoMessage()
		enc, err := proto.Marshal(pb)
		if err != nil {
			continue
		}
		cache.Put(modules.Account, addr.Bytes(), enc)
	}

	// Merge storage.
	for addr, slots := range dl.storage {
		for key, val := range slots {
			// Use the same composite key format as CachedStateReader.
			// We need the account's incarnation, but the diff layer stores
			// full storage values. For cache population we use incarnation 0
			// as a simplified key — SnapshotStateReader bypasses this path
			// for storage. The primary benefit is for account-level caching.
			compositeKey := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), 0, key.Bytes())
			cache.Put(modules.Storage, compositeKey, val)
		}
	}
}

// Discard marks a layer and all layers that depend on it (children) as stale.
// Used during chain reorgs to invalidate a branch of the snapshot tree.
func (t *Tree) Discard(root types.Hash) {
	t.lock.Lock()
	defer t.lock.Unlock()

	layer, ok := t.layers[root]
	if !ok {
		return
	}

	// Collect all descendants.
	toDiscard := []types.Hash{root}
	toDiscard = t.collectChildren(root, toDiscard)

	// Mark stale and remove.
	for _, r := range toDiscard {
		if l, ok := t.layers[r]; ok {
			if dl, ok := l.(*DiffLayer); ok {
				dl.MarkStale()
			}
			delete(t.layers, r)
		}
	}
	_ = layer

	// Update metrics.
	diffCount, diffMem := t.sizeLocked()
	snapshotDiffLayers.Set(uint64(diffCount))
	snapshotDiffMemory.Set(diffMem)
}

// collectChildren recursively finds all layers whose parent root is in the
// toDiscard set. Caller must hold t.lock.
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

// sizeLocked returns diff layer count and memory. Caller must hold t.lock.
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
