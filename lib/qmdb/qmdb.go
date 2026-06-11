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

// leafHasherPool recycles blake3 hashers for hashLeaf — allocating a fresh
// hasher per call was ~7.5% of all process allocations at chain scale. A pool
// (not a shared global) keeps independent Trees safe to use from different
// goroutines (each Tree itself stays single-threaded).
var leafHasherPool = sync.Pool{New: func() any { return blake3.New(32, nil) }}

var leafDomain = [1]byte{0x01}

// hashLeaf is the leaf hash of a live entry (domain-separated from internal
// nodes by the 0x01 prefix).
func hashLeaf(keyHash Hash, value []byte) Hash {
	h := leafHasherPool.Get().(*blake3.Hasher)
	h.Reset()
	_, _ = h.Write(leafDomain[:])
	_, _ = h.Write(keyHash[:])
	_, _ = h.Write(value)
	var out Hash
	h.Sum(out[:0]) // cap(out) == Size(): Sum writes in place, no append-allocation
	leafHasherPool.Put(h)
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
	// nodes is the twig's complete binary Merkle heap (128 KiB): nodes[1] is the
	// twig root, internal nodes occupy [1, TwigSize), and the 2048 leaves occupy
	// [TwigSize, 2*TwigSize) — node j's children are 2j and 2j+1. Keeping the
	// internal nodes resident is what makes leaf changes O(log): setLeaf folds
	// just the 11-node path to the root instead of rebuilding all 4095 hashes
	// (profiling showed the full rebuild dominating conversion CPU at ~50%).
	// It is a POINTER so a sealed twig's node storage can be evicted (set to nil)
	// while its 32-byte root is retained; the nodes are rebuilt on demand from
	// the persisted leaf blob or cold entry log (see twig_evict.go).
	nodes  *[2 * TwigSize]Hash // nil = evicted (node storage freed, root kept)
	live   int
	root   Hash // current twig root (== nodes[1] when resident and not dirty)
	dirty  bool // internal nodes stale; full recompute needed (bulk-load/ForceDirty)
	pruned bool // nodes dropped (fully dead); root is the constant null-twig root
}

// nullLevel[h] is the root of an all-null subtree of height h (nullLevel[0] is
// the null leaf). Lets newTwig start with valid internal nodes for free.
var nullLevel = func() (lv [TwigHeight + 1]Hash) {
	for h := 1; h <= TwigHeight; h++ {
		lv[h] = hashNode(lv[h-1], lv[h-1])
	}
	return
}()

func newTwig() *twig {
	t := &twig{nodes: new([2 * TwigSize]Hash)} // zero-fill = null leaves
	// Internal depth d holds all-null subtrees of height TwigHeight-d.
	for d := 0; d < TwigHeight; d++ {
		v := nullLevel[TwigHeight-d]
		for j := 1 << d; j < 1<<(d+1); j++ {
			t.nodes[j] = v
		}
	}
	t.root = nullLevel[TwigHeight]
	return t
}

// leaf returns leaf `local`'s hash. The twig must be hydrated.
func (tw *twig) leaf(local uint64) Hash { return tw.nodes[TwigSize+local] }

// setLeaf writes leaf `local` and eagerly folds the 11-node path to the twig
// root — O(log) instead of the full 4095-hash rebuild. If the internal nodes are
// stale (dirty), only the leaf is written; recompute will rebuild everything.
func (tw *twig) setLeaf(local uint64, h Hash) {
	j := TwigSize + local
	tw.nodes[j] = h
	if tw.dirty {
		return
	}
	for j >>= 1; j >= 1; j >>= 1 {
		tw.nodes[j] = hashNode(tw.nodes[2*j], tw.nodes[2*j+1])
	}
	tw.root = tw.nodes[1]
}

// recompute rebuilds all internal nodes from the 2048 leaves (4095 hashes).
// Only needed after bulk leaf writes (hydration, LoadFrom, ForceDirty); steady-
// state updates go through setLeaf's O(log) path instead. The twig must be
// hydrated (nodes != nil); eviction never targets a dirty twig.
func (tw *twig) recompute() {
	for j := TwigSize - 1; j >= 1; j-- {
		tw.nodes[j] = hashNode(tw.nodes[2*j], tw.nodes[2*j+1])
	}
	tw.root = tw.nodes[1]
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

	// nDirtyTwigs counts twigs with stale internal nodes (dirty=true). Steady
	// state is ZERO (setLeaf keeps roots current), letting recomputeDirtyTwigs
	// return without scanning the O(numTwigs) forest on every block — the scan
	// was ~2.6% CPU at 24K twigs. Only ForceDirty and bulk snapshot import dirty
	// twigs, and both go through markTwigDirty.
	nDirtyTwigs int

	// deadFlushed collects slots whose entry row is already ON DISK (below the
	// evicted/flushed watermark) and was deactivated afterwards. Dead rows are
	// never read again (cold faults go through the live index, rehydration uses
	// the leaf blob / activeBits, index rebuild scans only active slots), so the
	// next FlushTo deletes them — the entry log then tracks the LIVE set instead
	// of the full append history. Rows orphaned by a crash between batches are
	// harmless garbage (never read), just unreclaimed.
	deadFlushed []uint64

	// Incremental upper tree (binary Merkle over twig roots). upper is a heap of
	// size 2*upCap (upCap = pow2 >= len(twigs)): upper[1] is the world root and
	// the twig roots occupy [upCap, upCap+len(twigs)), padded with nullHash. Twig
	// root changes are folded up O(log) paths (upDirty tracks which) instead of
	// rebuilding the whole level set per block — the rebuild is O(numTwigs),
	// which grows with chain history and dominated profiles at scale.
	upper     []Hash
	upCap     int
	upDirty   map[int]struct{} // twig IDs whose root changed since last fold
	upRebuild bool             // full upper rebuild needed (growth/bulk load)

	// rec, when non-nil, captures per-block undo data (deactivated slots' prior
	// state + the cursor watermark) for the sliding-window historical proofs in
	// undo.go. Nil (the default) adds zero overhead to Set/Delete.
	rec *BlockUndo

	// Batched leaf folding (batch.go): when batchFolding is on, leaf writes skip
	// the eager 11-hash path fold and record the touched leaf position instead;
	// foldTouched later folds the UNION of ancestor paths per twig with level-
	// by-level dedup. Appends land in consecutive slots, so a block's worth of
	// Sets shares almost all internal nodes — ~11 hashes/leaf drops toward the
	// ~1 amortized hash/leaf of a contiguous fill.
	batchFolding bool
	batchTouched []uint64 // absolute leaf slots written since the last fold
	batchScratch []uint32 // reusable per-twig node-index scratch
}

// New creates an empty tree with the default in-RAM index (flat open-addressing
// — see flatindex.go; the Go map's bucket pointer-chasing was ~2/3 of Set cost).
func New() *Tree {
	return &Tree{idx: newFlatIndex(), rootDirty: true}
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
		// current root (recompute would need its freed nodes).
		if tw != nil && !tw.pruned && tw.nodes != nil {
			t.markTwigDirty(tw)
		}
	}
	t.rootDirty = true
}

// markTwigDirty marks a twig's internal nodes stale, maintaining the dirty
// count that lets Root() skip the forest scan when nothing is stale.
func (t *Tree) markTwigDirty(tw *twig) {
	if !tw.dirty {
		tw.dirty = true
		t.nDirtyTwigs++
	}
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
	t.ensureHydrated(id) // rehydrate this twig's nodes if they were evicted
	tw := t.twigs[id]
	local := slot % TwigSize
	if tw.leaf(local) != nullHash {
		t.writeLeaf(tw, id, local, nullHash) // eager O(log) fold, or deferred in batch mode
		tw.live--
		t.markUpperDirty(id)
	}
	// Only resident entries carry a mutable active flag; cold entries derive it
	// from the index, so an evicted slot needs no record mutation here (its twig
	// leaf — the source of truth for the root — is nulled above).
	if slot >= t.entriesBase {
		if i := slot - t.entriesBase; i < uint64(len(t.entries)) {
			t.entries[i].active = false
		}
	} else {
		// The row for this slot is already on disk and is now dead: schedule its
		// deletion at the next flush (dead rows are never read again).
		t.deadFlushed = append(t.deadFlushed, slot)
	}
	t.rootDirty = true
}

// Set inserts or updates keyHash -> value (append-only: a new slot is consumed
// and the key's previous slot, if any, is deactivated).
func (t *Tree) Set(keyHash Hash, value []byte) {
	if old, ok := t.idx.Get(keyHash); ok {
		t.recordDeactivation(old, keyHash)
		t.deactivate(old)
	}
	slot := t.nextSlot
	t.nextSlot++
	tw := t.twigFor(slot)
	local := slot % TwigSize
	v := make([]byte, len(value))
	copy(v, value)
	t.setEntry(slot, entry{keyHash: keyHash, value: v, active: true})
	t.writeLeaf(tw, int(slot/TwigSize), local, hashLeaf(keyHash, v)) // eager O(log) fold, or deferred in batch mode
	tw.live++
	t.markUpperDirty(int(slot / TwigSize))
	t.idx.Put(keyHash, slot)
	t.rootDirty = true
}

// Delete removes keyHash (deactivates its slot). No-op if absent.
func (t *Tree) Delete(keyHash Hash) {
	if old, ok := t.idx.Get(keyHash); ok {
		t.recordDeactivation(old, keyHash)
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

// markUpperDirty records that twig id's root changed, so the next Root() folds
// just its path through the upper tree.
func (t *Tree) markUpperDirty(id int) {
	if t.upDirty == nil {
		t.upDirty = make(map[int]struct{}, 16)
	}
	t.upDirty[id] = struct{}{}
}

// ensureUpper sizes the upper heap to the twig count (next power of two);
// growth forces a full rebuild (rare: once per upCap doubling).
func (t *Tree) ensureUpper() {
	upCap := 1
	for upCap < len(t.twigs) {
		upCap <<= 1
	}
	if t.upper == nil || upCap != t.upCap {
		t.upCap = upCap
		t.upper = make([]Hash, 2*upCap)
		t.upRebuild = true
	}
}

// rebuildUpper recomputes the whole upper heap from the twig roots (padded to
// upCap with nullHash).
func (t *Tree) rebuildUpper() {
	for i := 0; i < t.upCap; i++ {
		if i < len(t.twigs) {
			t.upper[t.upCap+i] = t.twigs[i].root
		} else {
			t.upper[t.upCap+i] = nullHash
		}
	}
	for j := t.upCap - 1; j >= 1; j-- {
		t.upper[j] = hashNode(t.upper[2*j], t.upper[2*j+1])
	}
	t.upRebuild = false
	clear(t.upDirty)
}

// updateUpperPath folds one changed twig root up to upper[1] — O(log numTwigs).
func (t *Tree) updateUpperPath(id int) {
	j := t.upCap + id
	t.upper[j] = t.twigs[id].root
	for j >>= 1; j >= 1; j >>= 1 {
		t.upper[j] = hashNode(t.upper[2*j], t.upper[2*j+1])
	}
}

// Root returns the world state root, recomputing dirty twigs + the upper tree.
func (t *Tree) Root() Hash {
	t.foldTouched() // settle deferred batch leaves first (no-op when empty)
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
	// cores. (Steady state has no dirty twigs: setLeaf keeps roots current.)
	t.recomputeDirtyTwigs()
	t.ensureUpper()
	// Fold only the changed twig roots through the upper tree; full rebuild when
	// grown, bulk-loaded, or when most twigs changed anyway.
	if t.upRebuild || len(t.upDirty) >= t.upCap/2 {
		t.rebuildUpper()
	} else {
		for id := range t.upDirty {
			t.updateUpperPath(id)
		}
		clear(t.upDirty)
	}
	t.root = t.upper[1]
	t.rootDirty = false
	return t.root
}

// ParallelRoot toggles concurrent twig recomputation (default true). Exposed so
// benchmarks can compare against the sequential path.
var ParallelRoot = true

func (t *Tree) recomputeDirtyTwigs() {
	// Steady state has ZERO dirty twigs (setLeaf keeps roots current); the
	// maintained counter makes that case free — no O(numTwigs) forest scan per
	// block (the scan itself was ~2.6% CPU at 24K twigs, and the slice the
	// original version allocated before counting was 40% of ALL allocations).
	nDirty := t.nDirtyTwigs
	if nDirty == 0 {
		return
	}
	defer func() { t.nDirtyTwigs = 0 }() // every dirty twig is recomputed below
	if !ParallelRoot || nDirty == 1 {
		for id, tw := range t.twigs {
			if tw.dirty {
				tw.recompute()
				t.markUpperDirty(id)
			}
		}
		return
	}
	// Collect dirty twigs (and mark their upper paths — roots are changing).
	dirty := make([]*twig, 0, nDirty)
	for id, tw := range t.twigs {
		if tw.dirty {
			dirty = append(dirty, tw)
			t.markUpperDirty(id)
		}
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
