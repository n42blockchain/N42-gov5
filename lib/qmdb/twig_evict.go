// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Twig-leaf eviction (P3 memory tier 2). After the entry log is evicted
// (evict.go), the next O(history) resident cost is the twig leaf arrays: 32 B per
// slot ever appended (a 64 KiB array per 2048-slot twig). A SEALED twig — one
// that lies fully below the flushed watermark — is only ever read again to (a)
// produce a membership proof or (b) null one of its leaves when a key it holds is
// updated/deleted elsewhere. Both can rebuild the leaves on demand from the cold
// entry log, so a sealed twig keeps only its 32-byte root in RAM and drops the
// 64 KiB leaf array.
//
// Rehydration reconstructs leaf[local] = hashLeaf(key,value) for each slot whose
// entry is still the live one (index[key] == slot), exactly as the leaves were
// built originally — so the recomputed root is identical to the retained root.
// This is correct but not free: a first touch of a sealed twig in a batch costs a
// 2048-entry cold scan. Updates have strong temporal locality (hot keys live in
// recent, non-evicted twigs), so in practice few sealed twigs are touched per
// batch; the steady-state resident leaf cost falls to O(active + touched-this-
// batch) instead of O(history). (Storing twig internal nodes on disk for O(log)
// path-updates would remove the scan entirely — a later optimization.)

package qmdb

// LeafStore serves a twig's persisted leaf array (TwigSize*32 raw bytes) so an
// evicted twig rehydrates in ONE read instead of a 2048-entry cold scan. Optional:
// without it, ensureHydrated falls back to rebuilding leaves from the entry log.
type LeafStore interface {
	Leaves(twigID int) ([]byte, bool) // raw TwigSize*32 blob, or ok=false if absent
}

// SetLeafStore attaches a leaf-blob source (and enables FlushTo to persist leaf
// blobs). The engine backs it with the QMDBTwigLeaves MDBX table.
func (t *Tree) SetLeafStore(ls LeafStore) { t.leafStore = ls }

// ensureHydrated rebuilds a sealed twig's leaf array if it was evicted. It prefers
// a single LeafStore blob read; otherwise it reconstructs from the cold entry log.
// No-op for resident, absent, or pruned twigs (a pruned twig has no live leaves).
func (t *Tree) ensureHydrated(id int) {
	if id < 0 || id >= len(t.twigs) {
		return
	}
	tw := t.twigs[id]
	if tw == nil || tw.leaves != nil || tw.pruned {
		return
	}
	// Fast path: one blob read.
	if t.leafStore != nil {
		if blob, ok := t.leafStore.Leaves(id); ok && len(blob) == TwigSize*32 {
			a := new([TwigSize]Hash)
			live := 0
			for i := 0; i < TwigSize; i++ {
				copy(a[i][:], blob[i*32:(i+1)*32])
				if a[i] != nullHash {
					live++
				}
			}
			tw.leaves = a
			tw.live = live
			return
		}
	}
	// Fallback: reconstruct from the entry log (random reads).
	a := new([TwigSize]Hash)
	lo := uint64(id) * TwigSize
	live := 0
	for local := 0; local < TwigSize; local++ {
		slot := lo + uint64(local)
		if slot >= t.nextSlot {
			break
		}
		e, ok := t.entryAt(slot)
		if ok && e.active {
			a[local] = hashLeaf(e.keyHash, e.value)
			live++
		}
	}
	tw.leaves = a
	tw.live = live // reconcile (should already match; cheap to be exact)
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
	maxID := int(through / TwigSize) // twigs with id < maxID are fully below `through`
	if maxID > len(t.twigs) {
		maxID = len(t.twigs)
	}
	for id := 0; id < maxID; id++ {
		tw := t.twigs[id]
		if tw == nil || tw.leaves == nil || tw.pruned || tw.dirty {
			continue
		}
		tw.leaves = nil // free 64 KiB; root retained for the upper tree
	}
}

// NumTwigs reports the total number of twigs in the forest (grows with history).
func (t *Tree) NumTwigs() int { return len(t.twigs) }

// ResidentTwigLeaves reports how many twigs currently hold their leaf array in RAM
// (the rest keep only their root). Used by tests to assert the leaf footprint
// stays bounded as the twig count grows with history.
func (t *Tree) ResidentTwigLeaves() int {
	n := 0
	for _, tw := range t.twigs {
		if tw != nil && tw.leaves != nil {
			n++
		}
	}
	return n
}
