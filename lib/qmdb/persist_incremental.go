// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// INCREMENTAL reload of a persistent speculative computer (the miner's per-build
// path). LoadFromTrustedIndex already skips the per-slot entry rescan for sealed
// twigs, but it still REBUILDS THE WHOLE FOREST on every call: one twig object
// plus one TwigTable point read per twig, from twig 0, every block. That cost is
// a function of total history, not of what changed — measured live at ~0.5 s idle
// and ~2.75 s under load, ~70% of the leader's block time, while executing the
// block's transactions took ~0.17 s.
//
// LoadIncremental instead ADVANCES the already-loaded forest from the cursor it
// was loaded at to the store's current cursor, touching only what can have
// changed:
//
//   - twigs at/above the previously-active twig — they gained slots (and the
//     previously-active twig's leaf tree grew), so they are re-read in full;
//   - sealed twigs whose activeBits the new appends cleared. QMDB is append-only
//     but NOT immutable below the cursor: overwriting a key clears the live bit
//     of its OLD slot, which usually lies deep in history. Those twigs are found
//     by looking each newly appended slot's key up in the (trusted) index BEFORE
//     the mapping is repointed — the old slot is exactly the bit the canonical
//     writer cleared — and their metadata is then RE-READ from the store rather
//     than simulated, so the store stays the authority on liveness.
//
// Everything else is reused verbatim. What makes that safe is not the case
// analysis above but the final check: FlushTo persists the world root in the same
// transaction as the twig metadata, so the reload is only accepted when the
// reconstructed root reproduces the persisted root BYTE-FOR-BYTE. Any change this
// pass failed to notice — an in-window delete (which appends nothing at all), a
// revert-and-re-execute, a compaction — lands on a different root and is rejected,
// and the caller falls back to the (unchanged) full-forest paths. Conservative by
// construction: a missed twig can only cause a fallback, never a wrong root.

package qmdb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrIncrementalUnavailable: the incremental reload declined before touching the
// tree (preconditions not met, store rewound, no persisted root to verify
// against). Not an error condition — the caller just takes the full path.
var ErrIncrementalUnavailable = errors.New("qmdb: incremental reload unavailable")

// ErrIncrementalRootMismatch: the incrementally reloaded forest did not
// reproduce the world root persisted alongside the twig metadata. Something
// changed below the boundary that the delta scan could not observe; the tree is
// left in an unusable state and the caller MUST fall back to a full reload.
var ErrIncrementalRootMismatch = errors.New("qmdb: incremental reload did not reproduce the persisted world root")

func incrUnavailable(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrIncrementalUnavailable}, a...)...)
}

// LoadIncremental advances an already-loaded forest to the store's current
// layout without rebuilding the twigs that cannot have changed. prevCursor is
// the slot cursor this tree was last loaded at (the same value
// LoadFromTrustedIndex would be given); expectDelta is the live-bits-minus-index
// baseline (see LoadFromTrustedIndex).
//
// On success the tree is byte-identical to what a full reload would produce —
// verified against the persisted world root before returning. On ANY error the
// caller must fall back to LoadFromTrustedIndex / LoadFrom; the tree may be left
// partially advanced, which those paths replace wholesale.
func (t *Tree) LoadIncremental(g Getter, prevCursor uint64, expectDelta int) error {
	if err := t.loadIncremental(g, prevCursor); err != nil {
		return err
	}
	// Secondary guard, kept identical to the full trusted reload: the root check
	// covers the commitment, this covers the index that rides alongside it.
	if d := t.LiveBits() - t.idx.Len(); d != expectDelta {
		return fmt.Errorf("%w: live-bits-minus-index=%d, want %d (baseline)", ErrIndexReconcile, d, expectDelta)
	}
	return nil
}

func (t *Tree) loadIncremental(g Getter, prev uint64) error {
	// --- preconditions -----------------------------------------------------
	// The tree must sit EXACTLY where it was last loaded: the caller peels the
	// previous candidate's appends with ApplyUndo, which restores the cursor,
	// the twig count, the bits and the index. Anything else (first load, failed
	// peel, mid-run recovery) takes the full path.
	if prev == 0 {
		return incrUnavailable("no previous load cursor")
	}
	if t.nextSlot != prev {
		return incrUnavailable("tree cursor %d != previous load cursor %d", t.nextSlot, prev)
	}
	if t.hist != nil {
		return incrUnavailable("full-history recorder attached")
	}
	if t.rec != nil {
		return incrUnavailable("undo recording is active")
	}
	if t.nDirtyTwigs != 0 {
		return incrUnavailable("%d twigs have stale internal nodes", t.nDirtyTwigs)
	}
	if t.batchFolding || len(t.batchTouched) != 0 || len(t.leafJobs) != 0 || len(t.bitsTouched) != 0 {
		return incrUnavailable("deferred batch folding is pending")
	}
	oldNumTwigs := int((prev + TwigSize - 1) / TwigSize)
	if len(t.twigs) != oldNumTwigs {
		return incrUnavailable("twig count %d != %d implied by the loaded cursor", len(t.twigs), oldNumTwigs)
	}
	for id, tw := range t.twigs {
		if tw == nil {
			return incrUnavailable("twig %d is absent", id)
		}
	}

	// --- the store's current head ------------------------------------------
	nsRaw, err := g.GetOne(MetaTable, []byte("nextSlot"))
	if err != nil {
		return err
	}
	if len(nsRaw) != 8 {
		return incrUnavailable("store has no usable nextSlot metadata")
	}
	next := binary.BigEndian.Uint64(nsRaw)
	if next < prev {
		// The store was rewound (revert): slots below the loaded cursor were
		// truncated and old bits revived. Nothing here can be reused safely.
		return incrUnavailable("store cursor %d is below the loaded cursor %d (store rewound)", next, prev)
	}
	rootRaw, err := g.GetOne(MetaTable, []byte("root"))
	if err != nil {
		return err
	}
	if len(rootRaw) != 32 {
		// Without the persisted root there is no way to prove the incremental
		// result — decline rather than ship an unverified forest.
		return incrUnavailable("store has no usable root metadata (cannot self-verify)")
	}
	var storedRoot Hash
	copy(storedRoot[:], rootRaw)

	if t.cold == nil {
		t.cold = ColdReaderFromGetter(g)
	}
	if t.leafStore == nil {
		t.leafStore = LeafStoreFromGetter(g)
	}

	numTwigs := int((next + TwigSize - 1) / TwigSize)
	activeTwig := int((next - 1) / TwigSize)
	// The twig that held the last loaded slot gained slots (its frozen leaf tree
	// grew), so it and everything above it is re-read in full.
	reloadFrom := int((prev - 1) / TwigSize)

	// --- 1. re-read the grown tail, collecting the slots it killed ----------
	victims := make(map[int]struct{})
	unknownRows := 0
	onScanned := func(slot uint64, kh Hash, ok bool) {
		if !ok {
			// The row of a slot that died after it was flushed is reclaimed, so
			// its key — and therefore its predecessor — is unknowable here. If
			// that predecessor's bit really did change, the root check rejects
			// the reload; count it for the diagnostic.
			unknownRows++
			return
		}
		old, found := t.idx.Get(kh)
		if !found || old >= prev {
			return // fresh key, or a slot appended inside this same window
		}
		if vid := int(old / TwigSize); vid < reloadFrom {
			victims[vid] = struct{}{} // sealed twig whose live bit was cleared
		}
	}

	if numTwigs > len(t.twigs) {
		grown := make([]*twig, numTwigs)
		copy(grown, t.twigs)
		t.twigs = grown
	}
	for id := reloadFrom; id < numTwigs; id++ {
		if err := t.loadTwigFrom(g, id, next, prev, activeTwig, onScanned); err != nil {
			return err
		}
		t.markUpperDirty(id)
	}

	// --- 2. refresh the sealed twigs whose activeBits the appends changed ---
	for id := range victims {
		if err := t.refreshSealedTwig(g, id); err != nil {
			return err
		}
	}

	// --- 3. post-load bookkeeping (mirrors resetForLoad's end state) --------
	// Entry records are not retained: the window restarts empty at the append
	// cursor and cold serves everything below it. The dead-row reclaim lists and
	// the value arena describe the abandoned pre-reload tree — dropping them is
	// what keeps a rolled-back execution from deleting live rows later (see
	// resetForLoad).
	t.entries = nil
	t.entriesBase = next
	t.evicted = next
	t.nextSlot = next
	t.deadFlushed = nil
	t.stagedDead = nil
	t.arena = nil
	t.foldScratch = t.foldScratch[:0]
	t.rootDirty = true

	// --- 4. self-verification against the persisted root --------------------
	// FlushTo writes the world root in the same transaction as the twig
	// metadata, so this is a complete check of the reconstructed commitment: any
	// bit this pass failed to refresh changes a twig root and therefore the
	// world root.
	if got := t.Root(); got != storedRoot {
		return fmt.Errorf("%w: got %x, persisted %x (cursor %d->%d, twigs reloaded %d, sealed refreshed %d, unreadable new rows %d)",
			ErrIncrementalRootMismatch, got[:8], storedRoot[:8], prev, next, numTwigs-reloadFrom, len(victims), unknownRows)
	}
	return nil
}

// refreshSealedTwig re-reads a SEALED twig's metadata from the store and adopts
// its activeBits.
//
// A twig strictly below the previously-active one is fully appended, so under the
// split commitment its leaf tree is FROZEN: only the bitmap can change. That is
// asserted, not assumed — the stored leaf root must reproduce the retained one,
// and the recombined root must reproduce the stored root. The twig's node heap
// (resident or evicted) is left exactly as it was, since the leaves it holds are
// still current.
func (t *Tree) refreshSealedTwig(g Getter, id int) error {
	tw := t.twigs[id]
	meta, err := g.GetOne(TwigTable, be8(uint64(id)))
	if err != nil {
		return err
	}
	if len(meta) < 32+32+TwigSize/8 {
		if len(meta) >= 32+TwigSize/8 {
			return errPreSplitMeta
		}
		return incrUnavailable("sealed twig %d has no v2 metadata", id)
	}
	var storedRoot, storedLeafRoot Hash
	copy(storedRoot[:], meta[:32])
	copy(storedLeafRoot[:], meta[32:64])
	if storedLeafRoot != tw.leafRoot {
		return incrUnavailable("sealed twig %d frozen leaf root changed on disk (%x != retained %x)",
			id, storedLeafRoot[:8], tw.leafRoot[:8])
	}
	copy(tw.bits[:], meta[64:64+TwigSize/8])
	live := 0
	for _, b := range tw.bits {
		live += popcount8(b)
	}
	if tw.pruned && live != 0 {
		// A fully-dead twig had its node storage dropped; a revived slot needs
		// leaves this path cannot restore. Rare (revert territory) — decline.
		return incrUnavailable("sealed twig %d was pruned but has %d live slots again", id, live)
	}
	tw.live = live
	tw.bitsRoot = hashBits(&tw.bits)
	tw.root = hashNode(tw.leafRoot, tw.bitsRoot)
	if tw.root != storedRoot {
		return errTwigMetaInconsistent
	}
	tw.dirty = false
	tw.metaDirty = false
	t.markUpperDirty(id)
	return nil
}
