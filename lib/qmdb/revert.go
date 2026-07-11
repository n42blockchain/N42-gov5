// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Mutating single-block revert of the live tree — the missing inverse of a
// block's Set/Delete ops. ProofAt (undo.go) reconstructs PAST roots on scratch
// copies for proofs; ApplyUndo actually ROLLS THE LIVE TREE BACK, which is what
// a HotStuff same-height sibling switch / reorg needs: the QMDB root is a
// function of the append HISTORY, so re-executing a competing block on top of
// an un-reverted tree appends at shifted slots and forks the root permanently
// vs a node that only ever applied the winner. Reverting first restores the
// exact pre-block tree; re-applying the sibling then lands on identical slots
// on every node.
//
// The same append-only structure that makes ProofAt cheap makes the mutating
// revert exact:
//
//   - slots appended by the block occupy [PrevNextSlot, nextSlot): truncate the
//     cursor, null their leaves, clear their bits, drop the index mappings of
//     the ones still live;
//   - slots deactivated by the block are u.Entries: set their bit back,
//     restore the index mapping and (for resident slots) the entry record. A
//     slot dies at most once, so revivals never conflict.

package qmdb

import (
	"errors"
	"fmt"
)

// ApplyUndo rolls the live tree back across ONE block, using that block's undo
// record (the most recently applied block first — chain calls newest→oldest to
// revert deeper). After it returns, Root() equals the root the tree had
// immediately before the block executed, byte-exactly.
//
// Storage-side fixups (deleting the truncated slots' persisted entry rows,
// re-putting revived rows that dead-row reclamation removed, dropped-twig meta
// rows, the flushed cursor) are the caller's job — see the commitment-layer
// RevertBlock, which pairs this with the MDBX writes in one tx.
func (t *Tree) ApplyUndo(u *BlockUndo) error {
	if u == nil {
		return errors.New("qmdb: nil undo record")
	}
	if u.broken {
		return fmt.Errorf("qmdb: undo record is poisoned (entry log / index mismatch at record time: key %x -> slot %d unreadable; tree entriesBase=%d evicted=%d next=%d)",
			u.brokenKey[:8], u.brokenSlot, t.entriesBase, t.evicted, t.nextSlot)
	}
	if u.PrevNextSlot > t.nextSlot {
		return fmt.Errorf("qmdb: undo record is ahead of this tree (prev=%d next=%d)", u.PrevNextSlot, t.nextSlot)
	}
	if t.hist != nil {
		return errors.New("qmdb: ApplyUndo with a full-history recorder attached is not supported (history-layer revert unimplemented)")
	}
	if t.rec != nil {
		return errors.New("qmdb: ApplyUndo while undo recording is active")
	}

	t.Root() // settle deferred batch folding + dirty twigs onto a clean base

	prev := u.PrevNextSlot

	// --- 0. Precompute the boundary twig's pre-block leaf tree. ------------
	// scratchLeafTreeAt is the only fallible step (it may hit a stale/missing
	// leaf blob and return an error rather than panic). Do it BEFORE any
	// mutation so a failure returns with the live tree completely untouched —
	// the caller then future-queues/rejects the block instead of operating on a
	// half-reverted, inconsistent in-memory tree. Step 1 below only clears bits
	// (never leaf nodes), so the boundary leaves are identical here and there.
	twigCountPrev := 0
	if prev > 0 {
		twigCountPrev = int((prev-1)/TwigSize) + 1
	}
	var boundaryNodes *[2 * TwigSize]Hash
	boundaryID := -1
	if t.nextSlot > prev && twigCountPrev > 0 {
		bID := twigCountPrev - 1
		if uint64(bID) == prev/TwigSize { // prev cuts through this twig
			nodes, err := t.scratchLeafTreeAt(bID, prev)
			if err != nil {
				return fmt.Errorf("qmdb: revert boundary twig %d: %w", bID, err)
			}
			boundaryNodes = nodes
			boundaryID = bID
		}
	}

	// --- 1. Truncate the block's appends: [prev, nextSlot). ---------------
	// Only LIVE appended slots still hold an index mapping (a dead appended
	// slot was overwritten/deleted within the block itself and its bit is
	// already clear); live slots are always readable by invariant.
	for s := prev; s < t.nextSlot; s++ {
		id := int(s / TwigSize)
		tw := t.twigs[id]
		local := s % TwigSize
		if !tw.bit(local) {
			continue
		}
		e, ok := t.entryAt(s)
		if !ok {
			return fmt.Errorf("qmdb: live appended slot %d unreadable during revert", s)
		}
		if got, ok2 := t.idx.Get(e.keyHash); ok2 && got == s {
			t.idx.Delete(e.keyHash)
		}
		tw.setBit(local, false)
		tw.live--
		tw.metaDirty = true
	}

	// --- 2. Install the precomputed boundary twig + drop tail twigs. -------
	// The boundary twig (partially appended at prev) needs its in-window leaves
	// nulled; install the pre-block leaf tree computed in step 0 as the resident
	// node heap. Do this BEFORE dropping tail twigs (it indexes t.twigs).
	if boundaryNodes != nil {
		tw := t.twigs[boundaryID]
		tw.nodes = boundaryNodes
		tw.dirty = false
		tw.pruned = false
		tw.leafRoot = boundaryNodes[1]
		tw.refreshBitsRoot()
		tw.metaDirty = true
	}
	if len(t.twigs) > twigCountPrev {
		t.twigs = t.twigs[:twigCountPrev]
	}

	// --- 3. Truncate the cursor + resident entry window. -------------------
	t.nextSlot = prev
	if t.entriesBase <= prev {
		if keep := prev - t.entriesBase; keep < uint64(len(t.entries)) {
			t.entries = t.entries[:keep]
		}
	} else {
		// The whole resident window was appended in-window (post-flush evict
		// advanced the base past prev): drop it and restart at the cursor.
		t.entries = nil
		t.entriesBase = prev
	}
	if t.evicted > prev {
		t.evicted = prev
	}

	// --- 4. Revive the block's deactivations. ------------------------------
	touched := make(map[int]struct{}, len(u.Entries)+1)
	for i := range u.Entries {
		e := &u.Entries[i]
		if e.Slot >= prev {
			continue // appended AND deactivated within the block: gone with the truncation
		}
		id := int(e.Slot / TwigSize)
		tw := t.twigs[id]
		local := e.Slot % TwigSize
		if !tw.bit(local) {
			tw.setBit(local, true)
			tw.live++
		}
		tw.pruned = false // a fully-dead (pruned) twig has a live slot again
		tw.metaDirty = true
		touched[id] = struct{}{}
		t.idx.Put(e.KeyHash, e.Slot)
		if e.Slot >= t.entriesBase {
			v := make([]byte, len(e.Value))
			copy(v, e.Value)
			t.setEntry(e.Slot, entry{keyHash: e.KeyHash, value: v, active: true})
		}
		// Cancel any pending dead-row reclamation for the revived slot, so the
		// next flush doesn't delete the row this key now depends on.
		for j := 0; j < len(t.deadFlushed); j++ {
			if t.deadFlushed[j] == e.Slot {
				t.deadFlushed[j] = t.deadFlushed[len(t.deadFlushed)-1]
				t.deadFlushed = t.deadFlushed[:len(t.deadFlushed)-1]
				j--
			}
		}
	}
	// Recombine the committed roots of twigs whose bits changed (the boundary
	// twig already recombined above).
	for id := range touched {
		tw := t.twigs[id]
		if !tw.dirty {
			tw.refreshBitsRoot()
		}
	}

	// --- 5. Upper tree: twig count/roots changed — rebuild cleanly. --------
	t.upRebuild = true
	t.rootDirty = true
	return nil
}

// PutDeleter is the storage capability ApplyUndoWithStorage needs (kv.RwTx and
// the test map store both satisfy it).
type PutDeleter interface {
	Putter
	Deleter
}

// ApplyUndoWithStorage reverts one block on the live tree AND repairs the
// persisted positional layout in the same storage transaction:
//
//   - deletes the truncated appends' entry rows ([PrevNextSlot, flushedThrough)),
//   - re-puts revived slots' rows (dead-row reclamation deletes a row when its
//     slot dies after being flushed — the revived key needs it back; the row
//     content is immutable so the re-put is exact),
//   - deletes dropped twigs' meta + leaf-blob rows,
//   - rewrites the recovery meta (root, nextSlot).
//
// Returns the new flushed cursor (min(flushedThrough, PrevNextSlot)) — the
// caller must adopt it so the next FlushTo re-appends from the right position.
func (t *Tree) ApplyUndoWithStorage(pd PutDeleter, u *BlockUndo, flushedThrough uint64) (uint64, error) {
	if u == nil {
		return flushedThrough, errors.New("qmdb: nil undo record")
	}
	oldNext := t.nextSlot
	oldTwigCount := len(t.twigs)
	if err := t.ApplyUndo(u); err != nil {
		return flushedThrough, err
	}
	prev := u.PrevNextSlot

	// Truncated appends: remove their persisted rows (only the flushed ones
	// have rows; deleting an absent key is harmless on MDBX/map stores alike,
	// but stay precise anyway).
	hi := flushedThrough
	if oldNext < hi {
		hi = oldNext
	}
	for s := prev; s < hi; s++ {
		if err := pd.Delete(EntryTable, be8(s)); err != nil {
			return flushedThrough, err
		}
	}
	// Revived slots: restore their rows (idempotent — content is immutable).
	for i := range u.Entries {
		e := &u.Entries[i]
		if e.Slot >= prev || e.Slot >= flushedThrough {
			continue
		}
		val := make([]byte, 0, 32+len(e.Value))
		val = append(val, e.KeyHash[:]...)
		val = append(val, e.Value...)
		if err := pd.Put(EntryTable, be8(e.Slot), val); err != nil {
			return flushedThrough, err
		}
	}
	// Dropped twigs: their meta/leaf rows describe a layout that no longer
	// exists; a reload trusting them would resurrect the truncated tail.
	for id := len(t.twigs); id < oldTwigCount; id++ {
		if err := pd.Delete(TwigTable, be8(uint64(id))); err != nil {
			return flushedThrough, err
		}
		if err := pd.Delete(LeavesTable, be8(uint64(id))); err != nil {
			return flushedThrough, err
		}
	}
	if flushedThrough > prev {
		flushedThrough = prev
	}
	// Rewrite the surviving-but-modified twigs' meta/blobs and the recovery
	// meta (root, nextSlot) — exactly FlushTo's job: ApplyUndo marked every
	// touched twig metaDirty, and the entry-append range [flushedThrough,
	// nextSlot) is empty after the truncation, so this writes only the meta.
	// Without it a standalone revert (no re-execute in the same tx) would
	// reload the boundary twig from its stale post-block row.
	if _, _, err := t.FlushTo(pd, flushedThrough); err != nil {
		return flushedThrough, err
	}
	return flushedThrough, nil
}
