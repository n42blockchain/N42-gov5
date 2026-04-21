// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Multi-Version HashMap for Block-STM style parallel EVM execution.
//
// MVHashMap is a versioned key-value store where each write is tagged
// with (txIdx, incarnation). Reads resolve to the highest-indexed
// committed write whose txIdx is strictly less than the reader's txIdx,
// implementing the "optimistic read" semantics of Aptos Block-STM.
//
// Reference: https://arxiv.org/abs/2203.06871 (Block-STM, Aptos)
// Reference: https://github.com/risechain/pevm (RISE pevm, Rust)
//
// Scope & limitations (Phase 1 PoC):
//
//   - Keys are []byte (composite addr+slot for storage, 20B addr for
//     accounts). The map doesn't interpret them; callers encode.
//   - Values are []byte. Nil means "delete / slot-is-zero" (caller
//     decides the semantics).
//   - The map is layered OVER a base store (e.g. MDBX + PlainStateBuffer).
//     A Read returning NotFound means "fall through to the base store".
//   - Sharding: N-way by FNV-1a hash of the key to reduce contention.
//     Each shard is a sync.RWMutex-guarded map[string]*versionList.
//   - Each key's version list is a descending-sorted slice of entries.
//     Inserts: O(log n + n) (binary search + memmove). Reads: O(log n)
//     (binary search for first entry.txIdx < readerTxIdx). Typical
//     per-key versionList length is 1-3 (one writer per tx per interval)
//     so the asymptotic is irrelevant; absolute cost is nanoseconds.
//
// Block-STM states a reader can observe:
//
//   - NotFound: no write for this key at any txIdx < readerTxIdx.
//     Caller falls through to the base store.
//   - Value + writerTxIdx + writerIncarnation: a committed write is
//     visible. Reader records this dependency: if the writer later
//     aborts/reexecutes, this reader must reexecute too.
//   - Estimate: the relevant writer's entry is marked "estimate"
//     (still executing or pending validation). Reader must either
//     block or record dependency and abort speculative execution.

package state

import (
	"encoding/binary"
	"hash/fnv"
	"sort"
	"sync"
)

// ReadStatus classifies the outcome of a versioned read.
type ReadStatus uint8

const (
	// MVNotFound: no write visible at a txIdx < readerTxIdx. Caller
	// should fall through to the base store (MDBX + PlainStateBuffer).
	MVNotFound ReadStatus = iota
	// MVOk: a committed (non-estimate) write is visible. Reader should
	// use the returned value and record the (writerTxIdx, writerIncarnation)
	// as a read-set dependency so validation can detect changes later.
	MVOk
	// MVEstimate: the highest-indexed writer's entry is marked "estimate"
	// (the writer is mid-execution or has been invalidated). The reader
	// should abort speculation and wait for the writer to settle. The
	// returned writerTxIdx identifies the blocking writer.
	MVEstimate
)

// Version uniquely identifies an MVHashMap write.
type Version struct {
	TxIdx       int
	Incarnation uint32
}

// entry is one write within a key's version list.
type entry struct {
	txIdx      int
	incarn     uint32
	value      []byte
	isEstimate bool
}

// versionList holds all writes for a single key, sorted DESCENDING by
// txIdx. Descending order lets readers find the "highest txIdx < readerTxIdx"
// in O(log n) via sort.Search.
type versionList struct {
	mu    sync.RWMutex
	items []entry
}

// findIndexLocked returns the position in items where an entry with
// (txIdx, _) would live (descending order). Caller must hold mu.
func (v *versionList) findIndexLocked(txIdx int) int {
	return sort.Search(len(v.items), func(i int) bool {
		return v.items[i].txIdx <= txIdx
	})
}

// upsert inserts or replaces the entry for txIdx. Returns true if this
// was a new insertion, false if the existing entry at txIdx was replaced.
func (v *versionList) upsert(e entry) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	i := v.findIndexLocked(e.txIdx)
	if i < len(v.items) && v.items[i].txIdx == e.txIdx {
		v.items[i] = e
		return false
	}
	// Insert at position i (descending order preserved).
	v.items = append(v.items, entry{})
	copy(v.items[i+1:], v.items[i:])
	v.items[i] = e
	return true
}

// remove deletes the entry at txIdx if present. Returns true if removed.
func (v *versionList) remove(txIdx int) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	i := v.findIndexLocked(txIdx)
	if i >= len(v.items) || v.items[i].txIdx != txIdx {
		return false
	}
	v.items = append(v.items[:i], v.items[i+1:]...)
	return true
}

// markEstimate flags the entry at txIdx as estimate. Returns true if
// the entry existed.
func (v *versionList) markEstimate(txIdx int) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	i := v.findIndexLocked(txIdx)
	if i >= len(v.items) || v.items[i].txIdx != txIdx {
		return false
	}
	v.items[i].isEstimate = true
	return true
}

// read returns the highest-indexed entry with txIdx < readerTxIdx,
// along with its status flags. readerTxIdx is the CURRENT executing
// tx's index; reads of its OWN prior writes are NOT returned here
// (self-reads are handled by the caller's tx-local view).
func (v *versionList) read(readerTxIdx int) (val []byte, writerIdx int, writerInc uint32, estimate bool, found bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	// items is DESCENDING by txIdx; find first entry with txIdx < readerTxIdx.
	// sort.Search on ">=" target condition.
	i := sort.Search(len(v.items), func(i int) bool {
		return v.items[i].txIdx < readerTxIdx
	})
	if i >= len(v.items) {
		return nil, 0, 0, false, false
	}
	e := v.items[i]
	return e.value, e.txIdx, e.incarn, e.isEstimate, true
}

// MVHashMap is the versioned key-value store used by the Block-STM
// scheduler for parallel EVM execution.
type MVHashMap struct {
	shards []mvShard
	mask   uint32 // numShards - 1, for fast modulo (numShards is power of 2)
}

type mvShard struct {
	mu   sync.RWMutex
	data map[string]*versionList
}

// NewMVHashMap creates a map with the given number of shards. Shard
// count must be a power of 2; if not, it is rounded up.
func NewMVHashMap(shards int) *MVHashMap {
	if shards < 1 {
		shards = 1
	}
	// Round up to next power of 2.
	n := 1
	for n < shards {
		n <<= 1
	}
	m := &MVHashMap{
		shards: make([]mvShard, n),
		mask:   uint32(n - 1),
	}
	for i := range m.shards {
		m.shards[i].data = make(map[string]*versionList)
	}
	return m
}

// keyHash computes the shard index for a key.
func (m *MVHashMap) keyHash(key []byte) uint32 {
	h := fnv.New32a()
	h.Write(key)
	return h.Sum32() & m.mask
}

// getOrCreate returns the versionList for key, creating it if absent.
func (m *MVHashMap) getOrCreate(key []byte) *versionList {
	shardIdx := m.keyHash(key)
	s := &m.shards[shardIdx]
	s.mu.RLock()
	vl, ok := s.data[string(key)]
	s.mu.RUnlock()
	if ok {
		return vl
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if vl, ok = s.data[string(key)]; ok {
		return vl
	}
	vl = &versionList{}
	s.data[string(key)] = vl
	return vl
}

// get returns the versionList for key, or nil if absent.
func (m *MVHashMap) get(key []byte) *versionList {
	shardIdx := m.keyHash(key)
	s := &m.shards[shardIdx]
	s.mu.RLock()
	vl := s.data[string(key)]
	s.mu.RUnlock()
	return vl
}

// Write records (txIdx, incarnation, value) for key. Overwrites any
// existing entry for the same (key, txIdx); used during tx execution
// AND during re-execution (incarnation bumped).
func (m *MVHashMap) Write(key []byte, v Version, value []byte) {
	vl := m.getOrCreate(key)
	vl.upsert(entry{
		txIdx:      v.TxIdx,
		incarn:     v.Incarnation,
		value:      value,
		isEstimate: false,
	})
}

// Read returns the highest-indexed entry whose txIdx < readerTxIdx.
// Status distinguishes NotFound (fall through to base), Ok (use value),
// Estimate (writer still pending — reader must defer).
func (m *MVHashMap) Read(key []byte, readerTxIdx int) (value []byte, writerVer Version, status ReadStatus) {
	vl := m.get(key)
	if vl == nil {
		return nil, Version{}, MVNotFound
	}
	val, wIdx, wInc, est, found := vl.read(readerTxIdx)
	if !found {
		return nil, Version{}, MVNotFound
	}
	if est {
		return nil, Version{TxIdx: wIdx, Incarnation: wInc}, MVEstimate
	}
	return val, Version{TxIdx: wIdx, Incarnation: wInc}, MVOk
}

// MarkEstimate flags tx_i's write at key as estimate. Called when the
// scheduler aborts tx_i (validation failed) and is about to re-execute:
// concurrent readers seeing this entry know to treat it as provisional.
// No-op if the key or txIdx entry is absent.
func (m *MVHashMap) MarkEstimate(key []byte, txIdx int) {
	vl := m.get(key)
	if vl == nil {
		return
	}
	vl.markEstimate(txIdx)
}

// Delete removes tx_i's write at key. Called when tx_i's re-execution
// produces a write set DIFFERENT from the previous incarnation — the
// old key needs to be removed so later readers don't see stale writes.
// No-op if absent.
func (m *MVHashMap) Delete(key []byte, txIdx int) {
	vl := m.get(key)
	if vl == nil {
		return
	}
	vl.remove(txIdx)
}

// Len returns the approximate number of keys across all shards.
// Not snapshotted — racing inserts may over- or under-count.
func (m *MVHashMap) Len() int {
	n := 0
	for i := range m.shards {
		m.shards[i].mu.RLock()
		n += len(m.shards[i].data)
		m.shards[i].mu.RUnlock()
	}
	return n
}

// CommitIter is returned by Iter; callers use it to walk final committed
// writes in (key, txIdx) order for the commit phase.
type CommitEntry struct {
	Key   []byte
	Value []byte
	TxIdx int
}

// CollectHighestWrites iterates all keys and returns the HIGHEST-txIdx
// write for each key, for the commit phase. Called after all txs have
// successfully executed and validated. Skips estimate entries (there
// shouldn't be any at commit time, but defensive).
//
// Returned slice order is NOT stable — caller should sort for
// deterministic writes.
func (m *MVHashMap) CollectHighestWrites() []CommitEntry {
	out := make([]CommitEntry, 0, 1024)
	for si := range m.shards {
		s := &m.shards[si]
		s.mu.RLock()
		for k, vl := range s.data {
			vl.mu.RLock()
			// items is descending; items[0] is the highest txIdx.
			if len(vl.items) > 0 && !vl.items[0].isEstimate {
				out = append(out, CommitEntry{
					Key:   []byte(k),
					Value: vl.items[0].value,
					TxIdx: vl.items[0].txIdx,
				})
			}
			vl.mu.RUnlock()
		}
		s.mu.RUnlock()
	}
	return out
}

// EncodeBlockNumKey is a helper for constructing debug keys; not used
// in the hot path. Kept here so tests can easily build distinct keys.
func EncodeBlockNumKey(blockNum uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, blockNum)
	return b
}
