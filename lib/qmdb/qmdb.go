// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package qmdb is a QMDB-style authenticated store mapped onto N42's binary
// Blake3 Merkle tree (BMT): it replaces content-addressed random writes with an
// append-only, position-addressed twig forest plus a key->slot index. It is wired
// into replay-v2 as the --tree qmdb commitment.
//
// Memory is bounded for chain-scale use by three eviction tiers, all opt-in via a
// cold reader / injected index and transparent when absent:
//   - entry log: a sliding window; flushed entry records are evicted from RAM and
//     faulted back from the cold store (evict.go).
//   - twig leaves: a sealed twig keeps only its 32-byte root; its 64 KiB leaf
//     array is freed and rebuilt on demand from the cold store (twig_evict.go).
//   - live-key index: pluggable (index.go); the engine backs it with MDBX so it
//     lives off-heap and survives restarts without a rebuild.
//
// Structure (three layers):
//
//	worldRoot  = binary Merkle over twig roots (the "upper tree", in memory)
//	twigRoot_i = binary Merkle over 2048 leaf hashes (one twig = an 11-level
//	             binary subtree; empty/dead slots hash to nullHash)
//	leaf       = Blake3(0x01 || keyHash || value) for the LIVE entry in a slot
//
// Updates are append-only: setting a key appends a new entry to the next free
// slot and deactivates the key's previous slot (its leaf becomes nullHash; the
// entry record is never rewritten). The root therefore commits to exactly the
// set of live (key,value) entries AT THEIR SLOTS. Consequently the root is a
// deterministic function of the update HISTORY (not of the live key set alone):
// two histories ending in the same key set but inserting in a different order
// produce different roots. This is the QMDB property — snapshots ship slot
// positions, and cross-node agreement comes from identical replay, not from a
// canonical-by-key-set tree.
//
// NOT thread-safe; callers synchronize externally (matches IntraBlockState).
package qmdb

import (
	"runtime"
	"sync"

	"lukechampine.com/blake3"
	"lukechampine.com/blake3/guts"
)

const (
	// TwigSize is the number of slots per twig (2^TwigHeight). A twig is a
	// complete binary Merkle subtree over this many leaves.
	TwigSize = 2048
	// TwigHeight is log2(TwigSize) — the number of levels inside a twig.
	TwigHeight = 11
)

// Hash is a 32-byte Blake3 digest.
type Hash [32]byte

// nullHash marks an empty or deactivated slot. Zero is fine for the prototype.
var nullHash = Hash{}

// hashLeaf is the leaf hash of a live entry (domain-separated from internal
// nodes by the 0x01 prefix).
func hashLeaf(keyHash Hash, value []byte) Hash {
	h := blake3.New(32, nil)
	_, _ = h.Write([]byte{0x01})
	_, _ = h.Write(keyHash[:])
	_, _ = h.Write(value)
	var out Hash
	copy(out[:], h.Sum(nil))
	return out
}

// hashNode is Blake3(left || right) for internal nodes. Hot path: a twig
// recompute calls this 4095 times. blake3.Sum256(buf[:]) forces `buf` to escape
// to the heap (the compiler can't prove Sum512's general path doesn't retain the
// slice), which allocated ~190 GB/run and drove a third of CPU into GC. Since the
// input is always exactly one 64-byte block, we call the low-level single-block
// compression directly (identical to what Sum512 does for <=64-byte input, then
// truncated to 256 bits) — all by value, nothing escapes, zero allocations.
func hashNode(l, r Hash) Hash {
	var block [64]byte
	copy(block[:32], l[:])
	copy(block[32:], r[:])
	words := guts.CompressNode(guts.Node{
		CV:       guts.IV,
		Block:    guts.BytesToWords(block),
		BlockLen: 64,
		Flags:    guts.FlagChunkStart | guts.FlagChunkEnd | guts.FlagRoot,
	})
	wb := guts.WordsToBytes(words)
	var out Hash
	copy(out[:], wb[:32])
	return out
}

type entry struct {
	keyHash Hash
	value   []byte
	active  bool
}

type twig struct {
	// leaves is the 2048-leaf array (64 KiB). It is a POINTER so a sealed twig's
	// leaf storage can be evicted (set to nil) while its 32-byte root is retained;
	// the leaves are rebuilt on demand from the cold entry log (see twig_evict.go).
	leaves *[TwigSize]Hash // nil = evicted (leaf storage freed, root kept)
	live   int
	root   Hash
	dirty  bool
	pruned bool // leaves dropped (fully dead); root is the constant null-twig root
}

func newTwig() *twig {
	t := &twig{dirty: true, leaves: new([TwigSize]Hash)}
	return t // new([...]) zero-fills, and nullHash is the zero Hash
}

// recompute rebuilds the twig root from its 2048 leaves (cheap: 4095 hashes).
// The twig must be hydrated (leaves != nil); callers dirty a twig only after
// hydrating it, and eviction never targets a dirty twig.
func (tw *twig) recompute() {
	var buf [TwigSize]Hash
	buf = *tw.leaves
	n := TwigSize
	for n > 1 {
		for i := 0; i < n/2; i++ {
			buf[i] = hashNode(buf[2*i], buf[2*i+1])
		}
		n /= 2
	}
	tw.root = buf[0]
	tw.dirty = false
}

// Tree is the in-memory QMDB-twig forest.
//
// Memory bounding (P3): the entry log is a SLIDING WINDOW. entries[i] holds the
// absolute slot (entriesBase + i); slots below entriesBase have been evicted from
// RAM and are served from an optional ColdReader (the persisted entry log). This
// keeps the resident entry footprint O(unflushed window) instead of O(history).
// When cold is nil the window starts at 0 and never shifts — identical to the
// original all-in-RAM behavior, so existing callers/tests are unaffected.
type Tree struct {
	twigs       []*twig
	entries     []entry    // window: entries[i] is absolute slot entriesBase+i
	entriesBase uint64     // absolute slot of entries[0]; slots < base are cold
	cold        ColdReader // serves evicted entries (nil = no eviction)
	leafStore   LeafStore  // serves persisted twig leaf blobs for fast rehydration
	evicted     uint64     // count of slots evicted from RAM (for Stats)
	idx         Index      // keyHash -> global slot of the live entry (pluggable)
	nextSlot    uint64     // append cursor
	root        Hash
	rootDirty   bool
}

// New creates an empty tree with the default in-RAM index.
func New() *Tree {
	return &Tree{idx: newMapIndex(), rootDirty: true}
}

// entryAt returns the entry at an absolute slot, transparently reading the cold
// store for evicted slots. The active flag of a cold entry is DERIVED (a slot is
// live iff it is the indexed slot for its key), since cold records are immutable.
func (t *Tree) entryAt(slot uint64) (entry, bool) {
	if slot >= t.entriesBase {
		i := slot - t.entriesBase
		if i < uint64(len(t.entries)) {
			return t.entries[i], true
		}
		return entry{}, false
	}
	if t.cold == nil {
		return entry{}, false
	}
	kh, v, ok := t.cold.ColdEntry(slot)
	if !ok {
		return entry{}, false
	}
	// Derive liveness via comma-ok: an absent key must NOT alias slot 0 (the
	// zero value of a missing entry would otherwise mark cold slot 0 active).
	liveSlot, present := t.idx.Get(kh)
	return entry{keyHash: kh, value: v, active: present && liveSlot == slot}, true
}

// setEntry writes an entry at an absolute slot, growing the window as needed.
// slot must be >= entriesBase (appends only ever target the live cursor).
func (t *Tree) setEntry(slot uint64, e entry) {
	i := slot - t.entriesBase
	for uint64(len(t.entries)) <= i {
		t.entries = append(t.entries, entry{})
	}
	t.entries[i] = e
}

// LiveCount returns the number of live keys.
func (t *Tree) LiveCount() int { return t.idx.Len() }

// ForceDirty marks every twig dirty so the next Root() recomputes the whole
// forest. Intended for benchmarking the parallel-vs-sequential recompute path.
func (t *Tree) ForceDirty() {
	for _, tw := range t.twigs {
		// Only resident twigs can be recomputed; an evicted twig already holds its
		// current root (recompute would need its freed leaves).
		if tw != nil && !tw.pruned && tw.leaves != nil {
			tw.dirty = true
		}
	}
	t.rootDirty = true
}

// NextSlot exposes the append cursor (total entries ever appended) — for tests
// that assert the append-only invariant (it only grows).
func (t *Tree) NextSlot() uint64 { return t.nextSlot }

func (t *Tree) twigFor(slot uint64) *twig {
	id := int(slot / TwigSize)
	for len(t.twigs) <= id {
		t.twigs = append(t.twigs, newTwig())
	}
	return t.twigs[id]
}

func (t *Tree) deactivate(slot uint64) {
	id := int(slot / TwigSize)
	t.ensureHydrated(id) // rehydrate this twig's leaves if they were evicted
	tw := t.twigs[id]
	local := slot % TwigSize
	if tw.leaves[local] != nullHash {
		tw.leaves[local] = nullHash
		tw.live--
		tw.dirty = true
	}
	// Only resident entries carry a mutable active flag; cold entries derive it
	// from the index, so an evicted slot needs no record mutation here (its twig
	// leaf — the source of truth for the root — is nulled above).
	if slot >= t.entriesBase {
		if i := slot - t.entriesBase; i < uint64(len(t.entries)) {
			t.entries[i].active = false
		}
	}
	t.rootDirty = true
}

// Set inserts or updates keyHash -> value (append-only: a new slot is consumed
// and the key's previous slot, if any, is deactivated).
func (t *Tree) Set(keyHash Hash, value []byte) {
	if old, ok := t.idx.Get(keyHash); ok {
		t.deactivate(old)
	}
	slot := t.nextSlot
	t.nextSlot++
	tw := t.twigFor(slot)
	local := slot % TwigSize
	v := make([]byte, len(value))
	copy(v, value)
	t.setEntry(slot, entry{keyHash: keyHash, value: v, active: true})
	tw.leaves[local] = hashLeaf(keyHash, v)
	tw.live++
	tw.dirty = true
	t.idx.Put(keyHash, slot)
	t.rootDirty = true
}

// Delete removes keyHash (deactivates its slot). No-op if absent.
func (t *Tree) Delete(keyHash Hash) {
	if old, ok := t.idx.Get(keyHash); ok {
		t.deactivate(old)
		t.idx.Delete(keyHash)
	}
}

// Get returns the live value for keyHash (via the in-memory index — one map
// lookup, the prototype analogue of QMDB's 1-SSD-read path).
func (t *Tree) Get(keyHash Hash) ([]byte, bool) {
	if slot, ok := t.idx.Get(keyHash); ok {
		if e, ok2 := t.entryAt(slot); ok2 {
			return e.value, true
		}
	}
	return nil, false
}

// Root returns the world state root, recomputing dirty twigs + the upper tree.
func (t *Tree) Root() Hash {
	if !t.rootDirty {
		return t.root
	}
	if len(t.twigs) == 0 {
		t.root = nullHash
		t.rootDirty = false
		return t.root
	}
	// Recompute dirty twig roots in parallel — every twig is an independent
	// subtree (each goroutine mutates only its own twig), so this scales across
	// cores. The upper tree (over twig roots) is small and computed sequentially.
	t.recomputeDirtyTwigs()
	roots := make([]Hash, len(t.twigs))
	for i, tw := range t.twigs {
		roots[i] = tw.root
	}
	t.root = upperRoot(roots)
	t.rootDirty = false
	return t.root
}

// ParallelRoot toggles concurrent twig recomputation (default true). Exposed so
// benchmarks can compare against the sequential path.
var ParallelRoot = true

func (t *Tree) recomputeDirtyTwigs() {
	if !ParallelRoot {
		for _, tw := range t.twigs {
			if tw.dirty {
				tw.recompute()
			}
		}
		return
	}
	// Collect dirty twigs.
	dirty := make([]*twig, 0, len(t.twigs))
	for _, tw := range t.twigs {
		if tw.dirty {
			dirty = append(dirty, tw)
		}
	}
	if len(dirty) == 0 {
		return
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > len(dirty) {
		workers = len(dirty)
	}
	if workers <= 1 {
		for _, tw := range dirty {
			tw.recompute()
		}
		return
	}
	ch := make(chan *twig, len(dirty))
	for _, tw := range dirty {
		ch <- tw
	}
	close(ch)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tw := range ch {
				tw.recompute()
			}
		}()
	}
	wg.Wait()
}

// upperRoot is the binary Merkle root over twig roots, padded to a power of two
// with nullHash so proofs have a clean fixed-shape path.
func upperRoot(roots []Hash) Hash {
	np := 1
	for np < len(roots) {
		np <<= 1
	}
	level := make([]Hash, np)
	for i := range level {
		if i < len(roots) {
			level[i] = roots[i]
		} else {
			level[i] = nullHash
		}
	}
	n := np
	for n > 1 {
		for i := 0; i < n/2; i++ {
			level[i] = hashNode(level[2*i], level[2*i+1])
		}
		n /= 2
	}
	return level[0]
}
