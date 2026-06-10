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

// mapIndex is the default in-RAM index (a Go map). Identical to the original
// behavior; selected automatically by New() when no index is injected.
type mapIndex map[Hash]uint64

func newMapIndex() mapIndex { return make(mapIndex) }

func (m mapIndex) Get(k Hash) (uint64, bool) { s, ok := m[k]; return s, ok }
func (m mapIndex) Put(k Hash, s uint64)      { m[k] = s }
func (m mapIndex) Delete(k Hash)             { delete(m, k) }
func (m mapIndex) Len() int                  { return len(m) }

// SetIndex injects an external index implementation (e.g. MDBX-backed). It must be
// called on an EMPTY tree (before any Set) or with an index already holding the
// tree's live set; the engine sets it right after New()/before LoadFrom. The
// engine re-points an MDBX index at the current batch's tx via this each batch.
func (t *Tree) SetIndex(idx Index) { t.idx = idx }

// LiveCount returns the number of live keys (Index.Len()).
func (t *Tree) liveLen() int { return t.idx.Len() }
