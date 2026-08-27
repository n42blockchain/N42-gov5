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
// Process-wide cache for contract bytecode JUMPDEST analysis results, keyed by
// codeHash. Reads use immutable per-shard map snapshots and never take a lock;
// writes copy and publish only one of up to 256 shards. This matters because
// every JUMP/JUMPI can query the cache and a single RWMutex becomes a global
// serialization point on many-core witness replay.

package vm

import (
	"container/list"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
)

// CodeAnalysisCache caches contract bytecode analysis results (bitvec) across
// blocks, keyed by codeHash. This avoids re-running O(n) codeBitmap() analysis
// for contracts already seen in previous blocks. Inspired by Aptos AIP-107.
//
// Thread-safe for concurrent use by parallel EVM instances (Block-STM).
const maxCodeAnalysisShards = 256

type codeAnalysisShard struct {
	mu       sync.Mutex
	snapshot atomic.Pointer[map[types.Hash]*codeEntry]
	lru      *list.List
	capacity int
}

type CodeAnalysisCache struct {
	shards []codeAnalysisShard
}

type codeEntry struct {
	hash     types.Hash
	analysis []uint64
	fused    []byte // execution view (see fuse.go); nil until first Run
	blocks   *blockTable
	blocksJT *JumpTable // fork the block table was built for
	elem     *list.Element
}

// GlobalCodeAnalysisCache is the process-wide code analysis cache.
// Set during node initialization. May be nil (cache disabled).
var GlobalCodeAnalysisCache *CodeAnalysisCache

// NewCodeAnalysisCache creates a new code analysis LRU cache with the given capacity.
func NewCodeAnalysisCache(capacity int) *CodeAnalysisCache {
	if capacity <= 0 {
		capacity = 1
	}
	shardCount := capacity
	if shardCount > maxCodeAnalysisShards {
		shardCount = maxCodeAnalysisShards
	}
	c := &CodeAnalysisCache{shards: make([]codeAnalysisShard, shardCount)}
	base, remainder := capacity/shardCount, capacity%shardCount
	for i := range c.shards {
		shardCapacity := base
		if i < remainder {
			shardCapacity++
		}
		s := &c.shards[i]
		s.capacity = shardCapacity
		s.lru = list.New()
		initial := make(map[types.Hash]*codeEntry)
		s.snapshot.Store(&initial)
	}
	return c
}

func (c *CodeAnalysisCache) shard(codeHash types.Hash) *codeAnalysisShard {
	return &c.shards[int(codeHash[0])%len(c.shards)]
}

// Get retrieves a cached code analysis by codeHash.
// Returns the bitvec slice and true if found, or nil and false if not.
//
// The returned slice MUST NOT be mutated by callers. The bitvec is immutable
// after creation by codeBitmap() and is shared across concurrent readers.
func (c *CodeAnalysisCache) Get(codeHash types.Hash) ([]uint64, bool) {
	snapshot := c.shard(codeHash).snapshot.Load()
	entry, ok := (*snapshot)[codeHash]
	if !ok {
		return nil, false
	}
	return entry.analysis, true
}

// Put stores a code analysis result in the cache. If the cache is at capacity,
// the least recently used entry is evicted.
// entry returns the cached record for codeHash, or nil.
func (c *CodeAnalysisCache) entry(codeHash types.Hash) *codeEntry {
	snapshot := c.shard(codeHash).snapshot.Load()
	return (*snapshot)[codeHash]
}

func (c *CodeAnalysisCache) Put(codeHash types.Hash, analysis []uint64) {
	c.putFused(codeHash, analysis, nil, nil, nil)
}

// putFused stores the bitmap together with the execution view.
func (c *CodeAnalysisCache) putFused(codeHash types.Hash, analysis []uint64, fused []byte, blocks *blockTable, jt *JumpTable) {
	stored := make([]uint64, len(analysis))
	copy(stored, analysis)

	s := c.shard(codeHash)
	s.mu.Lock()
	defer s.mu.Unlock()

	current := *s.snapshot.Load()
	next := make(map[types.Hash]*codeEntry, len(current)+1)
	for hash, entry := range current {
		next[hash] = entry
	}
	if old, ok := next[codeHash]; ok {
		s.lru.Remove(old.elem)
		delete(next, codeHash)
	}
	for len(next) >= s.capacity {
		back := s.lru.Back()
		if back == nil {
			break
		}
		evicted := s.lru.Remove(back).(*codeEntry)
		delete(next, evicted.hash)
	}
	entry := &codeEntry{hash: codeHash, analysis: stored, fused: fused, blocks: blocks, blocksJT: jt}
	entry.elem = s.lru.PushFront(entry)
	next[codeHash] = entry
	s.snapshot.Store(&next)
}

// Size returns the number of entries in the cache.
func (c *CodeAnalysisCache) Size() int {
	size := 0
	for i := range c.shards {
		size += len(*c.shards[i].snapshot.Load())
	}
	return size
}
