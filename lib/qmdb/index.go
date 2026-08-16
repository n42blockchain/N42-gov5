// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// On-disk index abstraction (P3 memory tier 3 — the last O(history) term). The
// keyHash -> live-slot map is consulted on every Set (to find the slot to
// deactivate), Get, and cold-liveness check, and it holds one entry per LIVE key.
// At chain scale (hundreds of millions of live keys) a Go map (~48+ B/entry) is
// the dominant remaining RAM cost after entry- and twig-leaf eviction.
//
// The Index interface decouples the tree from how that map is stored. The default
// mapIndex keeps the original in-RAM behavior (used by every existing test and by
// callers that don't inject anything). The engine injects an MDBX-backed index
// (commitment.qmdbMDBXIndex) so the live-key map lives in the B+tree page cache
// instead of the Go heap — and, being persistent, it does not need rebuilding on
// resume.

package qmdb

import (
	"os"
	"strconv"
	"sync/atomic"
)

// Index maps a live key's hash to the global slot of its current entry. It is the
// reverse of the entry log (slot -> keyHash) and holds exactly the live key set.
// Put overwrites, Delete removes, Len returns the live-key count. Implementations
// are not required to be safe for concurrent use (the tree synchronizes).
type Index interface {
	Get(keyHash Hash) (slot uint64, ok bool)
	Put(keyHash Hash, slot uint64)
	Delete(keyHash Hash)
	Len() int
}

// IterableIndex is an Index that can enumerate its live set. Cross-checking a
// reload against an independent rebuild needs the actual keyHash -> slot
// mapping, not just its cardinality: an overwrite plus a delete inside the same
// twig can reproduce the world root exactly while leaving the index pointing at
// a dead slot, and Tree.Get does not consult the live bit — it would return the
// deleted value. Optional; the MDBX-backed index does not implement it.
type IterableIndex interface {
	Index
	// Range calls fn for every live mapping. Iteration stops early when fn
	// returns false. Order is unspecified.
	Range(fn func(keyHash Hash, slot uint64) bool)
}

// mapIndex is the default in-RAM index (a Go map). Identical to the original
// behavior; selected automatically by New() when no index is injected.
type mapIndex map[Hash]uint64

var _ IterableIndex = mapIndex(nil)

func newMapIndex() mapIndex { return make(mapIndex) }

// newMapIndexSized returns an index pre-sized for n keys.
//
// The in-RAM index is the largest single allocation in a loaded node: 780 MB
// of live heap, all of it Go map tables filled by the twig load. Grown from
// empty it doubles about two dozen times on the way to sixteen million keys,
// and every doubling allocates a new table and copies the old one -- roughly
// as much garbage again as the final table, and a transient peak well above
// it, at the one moment a node is already reading its whole tree off disk.
func newMapIndexSized(n int) mapIndex { return make(mapIndex, n) }

// NewMapIndex returns a fresh in-RAM index. Callers reloading a live tree from
// disk mid-run (recovery paths) must install one BEFORE LoadFrom: LoadFrom's
// "non-empty index → trust it, skip the rebuild scan" fast path exists for the
// persistent MDBX index only — a leftover in-RAM index reflects the pre-reload
// tree, and stale mappings make later overwrites deactivate the wrong slots.
func NewMapIndex() Index { return newMapIndex() }

// reserve returns an index pre-sized for n keys when the current one is an
// empty in-RAM map, and the current one otherwise.
//
// Only an EMPTY map can be replaced -- a populated one holds the tree's live
// set, and an MDBX-backed index is not ours to swap. Over-sizing is bounded by
// the caller: the hint is a slot count, which is an exact upper bound on live
// keys, so the table is never smaller than needed and at most as large as a
// tree with nothing deleted.
// It reports whether it replaced the index rather than leaving the caller to
// compare: the dynamic type here is a map, and comparing two interface values
// holding maps is a runtime panic, not a compile error.
func reserve(idx Index, n int) (Index, bool) {
	m, ok := idx.(mapIndex)
	if !ok || len(m) != 0 || n <= 0 {
		return idx, false
	}
	// N42_QMDB_INDEX_PRESIZE=0 keeps the grow-from-empty behaviour, so the two
	// can be compared on one binary.
	if v, err := strconv.ParseBool(os.Getenv("N42_QMDB_INDEX_PRESIZE")); err == nil && !v {
		return idx, false
	}
	return newMapIndexSized(n), true
}

// Index counters. Package-level and atomic because mapIndex is a bare map type
// with nowhere to hang state; they exist to answer whether the index earns the
// largest allocation in a loaded node -- 780 MB of live heap -- rather than to
// be read on the hot path.
var (
	idxGetHit  atomic.Uint64
	idxGetMiss atomic.Uint64
	idxPuts    atomic.Uint64
	idxDeletes atomic.Uint64
)

// IndexStats reports index usage since start.
func IndexStats() (hits, misses, puts, deletes uint64) {
	return idxGetHit.Load(), idxGetMiss.Load(), idxPuts.Load(), idxDeletes.Load()
}

func (m mapIndex) Get(k Hash) (uint64, bool) {
	s, ok := m[k]
	if ok {
		idxGetHit.Add(1)
	} else {
		idxGetMiss.Add(1)
	}
	return s, ok
}
func (m mapIndex) Put(k Hash, s uint64) { idxPuts.Add(1); m[k] = s }
func (m mapIndex) Delete(k Hash)             { idxDeletes.Add(1); delete(m, k) }
func (m mapIndex) Len() int                  { return len(m) }

func (m mapIndex) Range(fn func(Hash, uint64) bool) {
	for k, s := range m {
		if !fn(k, s) {
			return
		}
	}
}

// SetIndex injects an external index implementation (e.g. MDBX-backed). It must be
// called on an EMPTY tree (before any Set) or with an index already holding the
// tree's live set; the engine sets it right after New()/before LoadFrom. The
// engine re-points an MDBX index at the current batch's tx via this each batch.
func (t *Tree) SetIndex(idx Index) { t.idx = idx }

// LiveCount returns the number of live keys (Index.Len()).
func (t *Tree) liveLen() int { return t.idx.Len() }

// IndexLookup returns the slot the index currently maps keyHash to. It reads the
// index only — like Tree.Get, it does NOT consult the live bit — so a caller
// comparing two trees sees the raw mapping, including a stale one.
func (t *Tree) IndexLookup(keyHash Hash) (uint64, bool) { return t.idx.Get(keyHash) }

// IndexRange enumerates the live keyHash -> slot mappings, returning false when
// the installed index cannot be iterated (the MDBX-backed one). Diagnostic use
// only: the map it walks is the whole live key set.
func (t *Tree) IndexRange(fn func(keyHash Hash, slot uint64) bool) bool {
	it, ok := t.idx.(IterableIndex)
	if !ok {
		return false
	}
	it.Range(fn)
	return true
}
