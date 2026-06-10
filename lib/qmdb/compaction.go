// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Twig-lifecycle compaction (the QMDB space-reclamation mechanism). Append-only
// updates leave obsolete entries (deactivated slots) scattered through old
// twigs, so the on-disk/in-memory footprint grows with the number of state
// MODIFICATIONS rather than the live state size. Compaction bounds it: live
// entries from sparse old twigs are re-appended to the active twig, leaving the
// old twig fully dead, which is then pruned (its leaves dropped, its root
// becomes a constant null-twig root). Footprint then tracks live state + recent
// history instead of total history.
//
// Root behavior (two cases):
//   - Pruning a FULLY-DEAD twig (every slot already deactivated) is
//     ROOT-PRESERVING: a dead twig already contributes nullTwigRoot, so dropping
//     its leaf storage frees memory without changing the world root. Heavy
//     overwrite/delete workloads vacate whole old twigs, so most reclamation is
//     free this way.
//   - Moving LIVE entries out of a sparse-but-live twig re-appends them to new
//     slots, which DOES change the world root (the QMDB root is a function of the
//     physical twig layout, hence of the update AND compaction history — not a
//     canonical function of the key set).
//
// Consequently, for cross-node agreement the live-moving compaction schedule
// must be deterministic (same trigger + same order on every node — Compact moves
// entries in sorted key order), and snapshot-sync ships the post-compaction slot
// layout (SnapshotLog). This is the price of append-only O(1)-IO writes.

package qmdb

import "sort"

// nullTwigRoot is the root of an all-empty (pruned) twig — the precomputed
// all-null subtree root at twig height.
var nullTwigRoot = nullLevel[TwigHeight]

// markPruned drops a fully-dead twig's node storage, freeing memory; its root
// becomes the constant null-twig root so the upper tree stays well-formed.
func (t *Tree) markPruned(id int) {
	tw := t.twigs[id]
	if tw == nil || tw.live != 0 {
		return
	}
	// Drop the node heap; a pruned twig contributes nullTwigRoot.
	tw.nodes = nil
	tw.root = nullTwigRoot
	tw.dirty = false
	tw.pruned = true
	t.markUpperDirty(id)
	// Free the entry records for this twig's slot range (resident slots only;
	// evicted slots are already out of RAM).
	lo := uint64(id) * TwigSize
	hi := lo + TwigSize
	for s := lo; s < hi; s++ {
		if s < t.entriesBase {
			continue
		}
		i := s - t.entriesBase
		if i >= uint64(len(t.entries)) {
			break
		}
		t.entries[i] = entry{}
	}
}

// Compact re-appends live entries out of twigs whose live ratio is below
// liveRatioThreshold (e.g. 0.25), then prunes the emptied twigs. The active
// (highest-id) twig is never compacted. Returns the number of twigs pruned.
// Changes the world root (see file header).
func (t *Tree) Compact(liveRatioThreshold float64) int {
	if len(t.twigs) <= 1 {
		return 0
	}
	activeID := int((t.nextSlot) / TwigSize)
	// Collect sparse, non-pruned, non-active twigs.
	var sparse []int
	for id := 0; id < len(t.twigs); id++ {
		tw := t.twigs[id]
		if tw == nil || tw.pruned || id == activeID {
			continue
		}
		if tw.live == 0 {
			sparse = append(sparse, id) // already dead -> just prune
			continue
		}
		if float64(tw.live)/float64(TwigSize) < liveRatioThreshold {
			sparse = append(sparse, id)
		}
	}
	// Re-append live entries of sparse twigs in deterministic (slot) order so the
	// resulting layout — and thus the root — is reproducible across nodes.
	type mv struct {
		keyHash Hash
		value   []byte
	}
	var moves []mv
	for _, id := range sparse {
		lo := uint64(id) * TwigSize
		hi := lo + TwigSize
		for s := lo; s < hi; s++ {
			e, ok := t.entryAt(s) // faults evicted slots from cold
			if ok && e.active {
				moves = append(moves, mv{e.keyHash, e.value})
			}
		}
	}
	sort.Slice(moves, func(i, j int) bool {
		return compareHash(moves[i].keyHash, moves[j].keyHash) < 0
	})
	for _, m := range moves {
		t.Set(m.keyHash, m.value) // appends to active twig, deactivates old slot
	}
	// Now every sparse twig is fully dead -> prune.
	pruned := 0
	for _, id := range sparse {
		if t.twigs[id].live == 0 {
			t.markPruned(id)
			pruned++
		}
	}
	t.rootDirty = true
	return pruned
}

func compareHash(a, b Hash) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Stats reports footprint metrics for compaction validation.
type Stats struct {
	Twigs       int // total twig slots in the forest
	PrunedTwigs int // twigs reclaimed (no leaf storage)
	LiveTwigs   int // twigs still holding leaves
	LiveEntries int // live keys
	StoredSlots int // entry records still resident in memory
}

// Stats returns current footprint metrics.
func (t *Tree) Stats() Stats {
	s := Stats{Twigs: len(t.twigs), LiveEntries: t.idx.Len()}
	for _, tw := range t.twigs {
		if tw == nil {
			continue
		}
		if tw.pruned {
			s.PrunedTwigs++
		} else {
			s.LiveTwigs++
		}
	}
	for i := range t.entries {
		if t.entries[i].keyHash != (Hash{}) || t.entries[i].value != nil {
			s.StoredSlots++
		}
	}
	return s
}
