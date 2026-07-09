// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Twig-node eviction (P3 memory tier 2). After the entry log is evicted
// (evict.go), the next O(history) resident cost is the twig node heaps: a 128 KiB
// array (2048 leaves + 2047 internal nodes) per 2048-slot twig. A SEALED twig —
// one that lies fully below the flushed watermark — is only ever read again to
// (a) produce a membership proof or (b) null one of its leaves when a key it
// holds is updated/deleted elsewhere. Both can rebuild the heap on demand, so a
// sealed twig keeps only its 32-byte root in RAM and drops the node array.
//
// Rehydration restores the leaves from the persisted leaf blob (one read) or the
// cold entry log (leaf[local] = hashLeaf(key,value) for each slot whose entry is
// still the live one, index[key] == slot), then one recompute rebuilds the
// internal nodes — the recomputed root is identical to the retained root. First
// touch of a sealed twig in a batch therefore costs one blob read + a 4095-hash
// rebuild; after that, changes inside it are O(log) via setLeaf. Updates have
// strong temporal locality (hot keys live in recent, non-evicted twigs), so few
// sealed twigs are touched per batch and the steady-state resident cost is
// O(active + touched-this-batch) instead of O(history).

package qmdb

import "fmt"

// LeafStore serves a twig's persisted leaf array (TwigSize*32 raw bytes) so an
// evicted twig rehydrates in ONE read instead of a 2048-entry cold scan. Optional:
// without it, ensureHydrated falls back to rebuilding leaves from the entry log.
type LeafStore interface {
	Leaves(twigID int) ([]byte, bool) // raw TwigSize*32 blob, or ok=false if absent
}

// SetLeafStore attaches a leaf-blob source (and enables FlushTo to persist leaf
// blobs). The engine backs it with the QMDBTwigLeaves MDBX table.
func (t *Tree) SetLeafStore(ls LeafStore) { t.leafStore = ls }

// ensureHydrated rebuilds a sealed twig's node heap if it was evicted: the leaves
// come from a single LeafStore blob read (preferred) or the cold entry log, then
// one recompute restores the internal nodes (which proofs and setLeaf's O(log)
// path-updates read). No-op for resident, absent, or pruned twigs (a pruned twig
// has no live leaves).
//
// It returns an error (instead of panicking) when the reconstructed leaf tree
// does not reproduce the retained root — a stale/missing leaf blob for a twig
// with dead entries. On that error the twig is left EVICTED (nodes still nil), so
// the tree stays internally consistent and a revert caller can reject/future-queue
// the offending block rather than crash the whole node. Reaching this on the
// forward path is genuine corruption; those callers still fail loudly.
func (t *Tree) ensureHydrated(id int) error {
	if id < 0 || id >= len(t.twigs) {
		return nil
	}
	tw := t.twigs[id]
	if tw == nil || tw.nodes != nil || tw.pruned {
		return nil
	}
	a := new([2 * TwigSize]Hash)
	// Fast path: one blob read. Under the split commitment the blob holds the
	// FROZEN entry-hash leaves (dead slots keep their hash; liveness lives in
	// the resident activeBits, which eviction never drops).
	hydrated := false
	if t.leafStore != nil {
		if blob, ok := t.leafStore.Leaves(id); ok && len(blob) == TwigSize*32 {
			for i := 0; i < TwigSize; i++ {
				copy(a[TwigSize+i][:], blob[i*32:(i+1)*32])
			}
			hydrated = true
		}
	}
	if !hydrated {
		// Fallback: reconstruct from the entry log (random reads). Dead slots'
		// rows may have been deleted at flush — the frozen leaf tree is then
		// unrecoverable from the log alone; the root check below catches that
		// loudly instead of silently committing a wrong root.
		lo := uint64(id) * TwigSize
		for local := 0; local < TwigSize; local++ {
			slot := lo + uint64(local)
			if slot >= t.nextSlot {
				break
			}
			if e, ok := t.entryAt(slot); ok {
				a[TwigSize+local] = hashLeaf(e.keyHash, e.value)
			}
		}
	}
	// Rebuild the internal nodes on the SCRATCH array and check the recomputed
	// root against the retained one BEFORE committing to the twig, so a bad blob
	// leaves the twig evicted (recoverable) instead of half-hydrated with a wrong
	// root. This mirrors twig.recompute() but on `a`, not tw.nodes.
	for base := TwigSize / 2; base >= 1; base /= 2 {
		hashNodesRun(a[:], base, base)
	}
	leafRoot := a[1]
	bitsRoot := hashBits(&tw.bits)
	if hashNode(leafRoot, bitsRoot) != tw.root {
		return fmt.Errorf("qmdb: twig %d rehydration root mismatch — leaf blob missing/stale for a twig with dead entries", id)
	}
	// Proven identical to the retained root: commit the scratch heap.
	tw.nodes = a
	tw.leafRoot = leafRoot
	tw.bitsRoot = bitsRoot
	// live derives from the resident bitmap, not leaf nullness (dead leaves are
	// no longer null under the split commitment).
	live := 0
	for _, b := range tw.bits {
		live += popcount8(b)
	}
	tw.live = live
	tw.dirty = false
	return nil
}

func popcount8(b byte) int {
	n := 0
	for ; b != 0; b &= b - 1 {
		n++
	}
	return n
}

// EvictTwigsThrough frees the leaf arrays of every twig that lies fully below the
// `through` watermark (slots < through), keeping each twig's 32-byte root. It must
// be called only AFTER FlushTo has persisted those twigs' metadata, since an
// evicted twig is skipped by later flushes (its on-disk meta is treated as
// current). Dirty twigs (root not yet recomputed/persisted) and pruned twigs are
// left alone. Requires a cold reader (rehydration source).
func (t *Tree) EvictTwigsThrough(through uint64) {
	if t.cold == nil {
		return
	}
	t.foldTouched() // eviction retains tw.root — settle deferred batch leaves first
	maxID := int(through / TwigSize) // twigs with id < maxID are fully below `through`
	if maxID > len(t.twigs) {
		maxID = len(t.twigs)
	}
	for id := 0; id < maxID; id++ {
		tw := t.twigs[id]
		if tw == nil || tw.nodes == nil || tw.pruned || tw.dirty {
			continue
		}
		tw.nodes = nil // free 128 KiB; root retained for the upper tree
	}
}

// NumTwigs reports the total number of twigs in the forest (grows with history).
func (t *Tree) NumTwigs() int { return len(t.twigs) }

// ResidentTwigLeaves reports how many twigs currently hold their node heap in RAM
// (the rest keep only their root). Used by tests to assert the resident footprint
// stays bounded as the twig count grows with history.
func (t *Tree) ResidentTwigLeaves() int {
	n := 0
	for _, tw := range t.twigs {
		if tw != nil && tw.nodes != nil {
			n++
		}
	}
	return n
}
