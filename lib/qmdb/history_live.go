// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Undo-driven full-history journaling for the LIVE node.
//
// The inline recorder (history.go: SetHistoryRecorder + BeginBlock/EndBlock)
// writes key versions at Set time through whatever tx the store is pointed at.
// That fits the offline replay engine (one owner, one tx per batch, no
// speculation) but not a HotStuff node: blocks execute speculatively, are
// peeled, re-executed on sibling switches and only persisted by the block
// write. The live node therefore never attaches the inline recorder. Instead
// the block write journals history from the block's undo record, in the same
// tx that persists the undo record itself:
//
//   - undo.Entries are exactly the slots the block deactivated (a slot dies at
//     most once), so each gets death stamp = block;
//   - undo.AppendedKeys[i] is the key appended at PrevNextSlot+i, so each is
//     one key version;
//   - the post-block cursor and the settled tree's top band are read from the
//     live tree, which holds exactly the block's post-state at write time.
//
// RevertBlockHistory is the exact inverse, applied by every path that unwinds a
// persisted block (sibling switch, three-state realign), again from that
// block's undo record and in the same tx.
//
// Because no recorder is attached, ApplyUndo / incremental reloads keep working
// unchanged on the live tree. The only tree-level difference is
// SetRetainDeadEntryRows: historical proofs need the values of dead slots, so
// dead entry rows are kept on disk (archival retention), exactly what an
// attached recorder already forces.

package qmdb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// HistoryReverter is the store capability RevertBlockHistory needs on top of
// HistoryStore (the MDBX store implements it).
type HistoryReverter interface {
	HistoryStore
	DeleteBlockPos(block uint64) error
	DeleteTopBand(block uint64) error
	DeleteKeyVersion(keyHash Hash, pos uint64) error
}

// ErrHistoryUndoIncomplete marks an undo record that cannot drive the history
// journal (poisoned at record time, or not covering the block's appends).
var ErrHistoryUndoIncomplete = errors.New("qmdb history: undo record does not describe the block completely")

// ErrHistoryInconsistent marks a journal that disagrees with the undo record
// being applied (a slot stamped dead at another block).
var ErrHistoryInconsistent = errors.New("qmdb history: journal inconsistent with the undo record")

// SetRetainDeadEntryRows keeps the persisted entry rows of dead slots (no
// dead-row reclamation, no dead-at-flush row elision). Historical proofs read
// the value of a slot that has since died from its row. An attached history
// recorder implies the same; this switch is for the undo-driven live journal.
func (t *Tree) SetRetainDeadEntryRows(on bool) { t.archiveRows = on }

// RetainsDeadEntryRows reports whether dead entry rows are kept on disk.
func (t *Tree) RetainsDeadEntryRows() bool { return t.retainDeadRows() }

func (t *Tree) retainDeadRows() bool { return t.hist != nil || t.archiveRows }

// TopBandRecord settles the tree and packs its top band in the journal format
// ([upCap 8B LE] + upper[1:min(topBandNodes, 2*upCap))).
func (t *Tree) TopBandRecord() []byte {
	t.Root() // settle twig + upper heaps
	t.ensureUpper()
	n := topBandNodes
	if 2*t.upCap < n {
		n = 2 * t.upCap
	}
	packed := make([]byte, 8, 8+(n-1)*32)
	binary.LittleEndian.PutUint64(packed, uint64(t.upCap))
	for j := 1; j < n; j++ {
		packed = append(packed, t.upper[j][:]...)
	}
	return packed
}

// UndoIsEmpty reports whether the record describes a block that changed
// nothing (no appends, no deactivations).
func UndoIsEmpty(u *BlockUndo) bool {
	return u != nil && len(u.Entries) == 0 && len(u.AppendedKeys) == 0
}

// checkHistoryUndo validates that u can drive the journal for a block whose
// post-block cursor is endNext.
func checkHistoryUndo(block uint64, u *BlockUndo, endNext uint64) error {
	if u == nil {
		return fmt.Errorf("%w: block %d has no undo record", ErrHistoryUndoIncomplete, block)
	}
	if u.broken {
		return fmt.Errorf("%w: block %d undo record is poisoned (slot %d)", ErrHistoryUndoIncomplete, block, u.brokenSlot)
	}
	if u.PrevNextSlot+uint64(len(u.AppendedKeys)) != endNext {
		return fmt.Errorf("%w: block %d appended %d keys from slot %d but the cursor is at %d",
			ErrHistoryUndoIncomplete, block, len(u.AppendedKeys), u.PrevNextSlot, endNext)
	}
	if block > uint64(^uint32(0)) {
		return fmt.Errorf("qmdb history: block %d out of the stampable range [0, 2^32)", block)
	}
	if block == 0 && len(u.Entries) > 0 {
		// Stamp 0 means "alive"; a genesis deactivation cannot be journaled.
		return fmt.Errorf("qmdb history: block 0 deactivates %d slots (stamp 0 is reserved for alive)", len(u.Entries))
	}
	for i := range u.Entries {
		if u.Entries[i].Slot >= endNext {
			return fmt.Errorf("%w: block %d deactivates slot %d beyond the cursor %d",
				ErrHistoryUndoIncomplete, block, u.Entries[i].Slot, endNext)
		}
	}
	return nil
}

// RecordBlockHistory journals one block from its undo record: death stamps of
// the slots it deactivated, its key versions, its end cursor endNext and its
// top band (TopBandRecord of the post-block tree). Blocks must be journaled in
// increasing order; a block that changed nothing may be skipped (the floor
// lookups then resolve it to the previous record, which describes the same
// state).
func RecordBlockHistory(store HistoryStore, block uint64, u *BlockUndo, endNext uint64, band []byte) error {
	if err := checkHistoryUndo(block, u, endNext); err != nil {
		return err
	}
	if len(band) < 8 {
		return fmt.Errorf("qmdb history: block %d top band record too short (%d bytes)", block, len(band))
	}
	if err := updateDeathStamps(store, u, func(cur uint32, slot uint64) (uint32, error) {
		if cur != aliveStamp && uint64(cur) != block {
			return 0, fmt.Errorf("%w: slot %d already died at block %d, block %d deactivates it again", ErrHistoryInconsistent, slot, cur, block)
		}
		return uint32(block), nil
	}); err != nil {
		return err
	}
	for i := range u.AppendedKeys {
		if err := store.AppendKeyVersion(u.AppendedKeys[i], u.PrevNextSlot+uint64(i)); err != nil {
			return err
		}
	}
	if err := store.PutBlockPos(block, endNext); err != nil {
		return err
	}
	return store.PutTopBand(block, band)
}

// RevertBlockHistory removes one block's journal (the exact inverse of
// RecordBlockHistory for the same undo record). Revert newest first.
func RevertBlockHistory(store HistoryReverter, block uint64, u *BlockUndo) error {
	if u == nil {
		return fmt.Errorf("%w: block %d has no undo record", ErrHistoryUndoIncomplete, block)
	}
	if u.broken {
		return fmt.Errorf("%w: block %d undo record is poisoned (slot %d)", ErrHistoryUndoIncomplete, block, u.brokenSlot)
	}
	if err := updateDeathStamps(store, u, func(cur uint32, slot uint64) (uint32, error) {
		if cur != aliveStamp && uint64(cur) != block {
			return 0, fmt.Errorf("%w: reverting block %d but slot %d died at block %d", ErrHistoryInconsistent, block, slot, cur)
		}
		return aliveStamp, nil
	}); err != nil {
		return err
	}
	for i := range u.AppendedKeys {
		if err := store.DeleteKeyVersion(u.AppendedKeys[i], u.PrevNextSlot+uint64(i)); err != nil {
			return err
		}
	}
	// Revived slots must carry their version. A journal kept from genesis
	// already has it (recorded at append); a journal seeded mid-chain
	// (SeedHistory) only knows the versions live at the seed height, so
	// reverting the seed block itself revives slots it never recorded.
	for i := range u.Entries {
		e := &u.Entries[i]
		if e.Slot >= u.PrevNextSlot {
			continue // appended within the block: truncated, not revived
		}
		vs, err := store.KeyVersions(e.KeyHash)
		if err != nil {
			return err
		}
		if !containsU64(vs, e.Slot) {
			if err := store.AppendKeyVersion(e.KeyHash, e.Slot); err != nil {
				return err
			}
		}
	}
	if err := store.DeleteBlockPos(block); err != nil {
		return err
	}
	return store.DeleteTopBand(block)
}

func containsU64(s []uint64, v uint64) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// SeedHistory starts a journal at height `start` from the tree's current
// state (which must be the post-state of block `start`): every live key gets
// its live slot as a version, every dead slot is stamped dead at `start`, and
// the cursor and top band are recorded for `start`. Heights >= start are then
// provable exactly; heights below are not (the caller refuses them).
//
// Slots that died before `start` get stamp `start` rather than their real
// death block: indistinguishable for any height >= start. A genesis seed
// (start 0) cannot stamp a dead slot (stamp 0 means alive) and is refused if
// one exists. The store must not hold journal rows yet.
func SeedHistory(store HistoryStore, t *Tree, start uint64) error {
	if start > uint64(^uint32(0)) {
		return fmt.Errorf("qmdb history: seed block %d out of the stampable range", start)
	}
	t.Root() // settle before reading bits / the top band
	var werr error
	if !t.IndexRange(func(kh Hash, slot uint64) bool {
		werr = store.AppendKeyVersion(kh, slot)
		return werr == nil
	}) {
		// Non-iterable index: walk the live slots and read each key back
		// from its entry (resident or cold row).
		for slot := uint64(0); slot < t.nextSlot; slot++ {
			if active, _ := t.SlotActive(slot); !active {
				continue
			}
			e, ok := t.entryAt(slot)
			if !ok || e.hashOnly {
				return fmt.Errorf("qmdb history: live slot %d has no readable entry while seeding", slot)
			}
			if werr = store.AppendKeyVersion(e.keyHash, slot); werr != nil {
				break
			}
		}
	}
	if werr != nil {
		return werr
	}
	for twigID := uint64(0); twigID*TwigSize < t.nextSlot; twigID++ {
		var blob []byte
		for local := uint64(0); local < TwigSize; local++ {
			slot := twigID*TwigSize + local
			if slot >= t.nextSlot {
				break
			}
			active, known := t.SlotActive(slot)
			if !known {
				return fmt.Errorf("qmdb history: slot %d liveness unknown while seeding", slot)
			}
			if active {
				continue
			}
			if start == 0 {
				return fmt.Errorf("qmdb history: slot %d is dead at genesis; a genesis seed cannot stamp it", slot)
			}
			if blob == nil {
				blob = make([]byte, TwigSize*4)
			}
			binary.LittleEndian.PutUint32(blob[local*4:], uint32(start))
		}
		if blob != nil {
			if err := store.PutDeathStamps(twigID, blob); err != nil {
				return err
			}
		}
	}
	if err := store.PutBlockPos(start, t.nextSlot); err != nil {
		return err
	}
	return store.PutTopBand(start, t.TopBandRecord())
}

// updateDeathStamps applies f to the stamp of every slot in u.Entries, one
// read-modify-write per touched twig blob.
func updateDeathStamps(store HistoryStore, u *BlockUndo, f func(cur uint32, slot uint64) (uint32, error)) error {
	if len(u.Entries) == 0 {
		return nil
	}
	byTwig := make(map[uint64][]uint64, 4)
	order := make([]uint64, 0, 4)
	for i := range u.Entries {
		s := u.Entries[i].Slot
		tw := s / TwigSize
		if _, ok := byTwig[tw]; !ok {
			order = append(order, tw)
		}
		byTwig[tw] = append(byTwig[tw], s%TwigSize)
	}
	for _, twigID := range order {
		old, err := store.GetDeathStamps(twigID)
		if err != nil {
			return err
		}
		blob := make([]byte, TwigSize*4) // never alias the store's memory
		copy(blob, old)
		for _, local := range byTwig[twigID] {
			cur := binary.LittleEndian.Uint32(blob[local*4:])
			nv, err := f(cur, twigID*TwigSize+local)
			if err != nil {
				return err
			}
			binary.LittleEndian.PutUint32(blob[local*4:], nv)
		}
		if err := store.PutDeathStamps(twigID, blob); err != nil {
			return err
		}
	}
	return nil
}

// ProofAtHeightFrom is ProofAtHeight against a read-only history store, for a
// tree that has no recorder attached (the RPC prover's reloaded forest, which
// the live node journals through RecordBlockHistory). Same contract: the
// returned root MUST be checked against the trusted root of block h.
func (t *Tree) ProofAtHeightFrom(store HistoryStore, keyHash Hash, h uint64) (*Proof, Hash, bool, error) {
	if store == nil {
		return nil, Hash{}, false, errors.New("qmdb history: no store")
	}
	return t.proofAtHeight(store, store.GetDeathStamps, keyHash, h)
}
