// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Sliding-window historical proofs. The QMDB world root is history-dependent
// (it commits to live entries AT THEIR APPEND SLOTS), so a from-scratch rebuild
// cannot reproduce a past root. But the append-only structure makes RECENT
// roots cheap: a slot's (keyHash, value) never changes after it is appended —
// only its liveness flips, exactly once, live → dead. The whole difference
// between the tree at head N and at N-j is therefore:
//
//   - slots appended in (N-j, N]   → null leaves at N-j (cursor truncation)
//   - slots deactivated in (N-j, N] → restored leaves at N-j (the undo entries)
//
// Recording (slot, keyHash, oldValue) for each deactivation plus the cursor
// watermark per block — typically a few hundred bytes — is enough to rebuild
// any root in the window EXACTLY and serve membership proofs against it. This
// gives eth_getProof a recent-blocks window (e.g. 32 or 64) at ~zero storage,
// the QMDB analogue of geth's recent-state proof window.

package qmdb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// UndoEntry records one slot deactivated during a block: the slot was LIVE with
// (KeyHash, Value) immediately before the block. A slot dies at most once ever
// (slots are never reused), so entries never conflict across blocks.
type UndoEntry struct {
	Slot    uint64
	KeyHash Hash
	Value   []byte
}

// BlockUndo is one block's undo record.
type BlockUndo struct {
	PrevNextSlot uint64 // append cursor BEFORE the block's operations
	Entries      []UndoEntry

	// broken poisons the record when a deactivated slot's entry could not be
	// read back at record time (entry log / index disagreement) — ProofAt
	// refuses poisoned records instead of producing a wrong historical root.
	broken bool
}

// StartUndoRecording begins capturing undo data for the operations that follow
// (one block's worth). Nested starts overwrite the previous record.
func (t *Tree) StartUndoRecording() {
	t.rec = &BlockUndo{PrevNextSlot: t.nextSlot}
}

// StopUndoRecording ends the capture and returns the block's undo record
// (nil if recording was never started).
func (t *Tree) StopUndoRecording() *BlockUndo {
	r := t.rec
	t.rec = nil
	return r
}

// recordDeactivation captures a slot's pre-deactivation state into the active
// undo record. Called by Set/Delete BEFORE the deactivation. The slot is live
// here (the index points at it), so its entry is always readable — live rows
// are never pruned from the cold log.
func (t *Tree) recordDeactivation(slot uint64, keyHash Hash) {
	if t.rec == nil {
		return
	}
	e, ok := t.entryAt(slot)
	if !ok {
		// Live slots are always readable; reaching here means the entry log and
		// index disagree. Poison the record rather than silently producing wrong
		// historical roots.
		t.rec.broken = true
		return
	}
	v := make([]byte, len(e.value))
	copy(v, e.value)
	t.rec.Entries = append(t.rec.Entries, UndoEntry{Slot: slot, KeyHash: keyHash, Value: v})
}

const blockUndoVersion = 0x01

// Marshal encodes the undo record:
//
//	[0] version | uvarint prevNextSlot | uvarint count
//	per entry: uvarint slot | keyHash 32B | uvarint valueLen | value
func (u *BlockUndo) Marshal() []byte {
	size := 1 + 2*binary.MaxVarintLen64
	for _, e := range u.Entries {
		size += binary.MaxVarintLen64*2 + 32 + len(e.Value)
	}
	out := make([]byte, 0, size)
	out = append(out, blockUndoVersion)
	out = binary.AppendUvarint(out, u.PrevNextSlot)
	out = binary.AppendUvarint(out, uint64(len(u.Entries)))
	for _, e := range u.Entries {
		out = binary.AppendUvarint(out, e.Slot)
		out = append(out, e.KeyHash[:]...)
		out = binary.AppendUvarint(out, uint64(len(e.Value)))
		out = append(out, e.Value...)
	}
	return out
}

// UnmarshalBlockUndo decodes a record produced by Marshal.
func UnmarshalBlockUndo(b []byte) (*BlockUndo, error) {
	if len(b) < 3 || b[0] != blockUndoVersion {
		return nil, errors.New("qmdb: not a v1 block-undo record")
	}
	p := b[1:]
	uvarint := func() (uint64, error) {
		v, n := binary.Uvarint(p)
		if n <= 0 {
			return 0, errors.New("qmdb: block-undo bad uvarint")
		}
		p = p[n:]
		return v, nil
	}
	u := &BlockUndo{}
	var err error
	if u.PrevNextSlot, err = uvarint(); err != nil {
		return nil, err
	}
	count, err := uvarint()
	if err != nil {
		return nil, err
	}
	u.Entries = make([]UndoEntry, 0, count)
	for i := uint64(0); i < count; i++ {
		var e UndoEntry
		if e.Slot, err = uvarint(); err != nil {
			return nil, err
		}
		if len(p) < 32 {
			return nil, errors.New("qmdb: block-undo truncated at keyHash")
		}
		copy(e.KeyHash[:], p[:32])
		p = p[32:]
		vl, err := uvarint()
		if err != nil {
			return nil, err
		}
		if uint64(len(p)) < vl {
			return nil, errors.New("qmdb: block-undo truncated at value")
		}
		e.Value = make([]byte, vl)
		copy(e.Value, p[:vl])
		p = p[vl:]
		u.Entries = append(u.Entries, e)
	}
	if len(p) != 0 {
		return nil, fmt.Errorf("qmdb: block-undo %d trailing bytes", len(p))
	}
	return u, nil
}

// ProofAt produces a membership proof for keyHash against the world root as of
// the state BEFORE the blocks covered by undos. undos must be the consecutive
// per-block undo records from oldest to newest, covering (target, head] — i.e.
// undos[0] is the block right after the target state, undos[len-1] is the head
// block. The returned root is the recomputed target root; the caller MUST check
// it against the trusted header root for the target block.
//
// found=false means the key was absent at the target (no membership proof);
// the target root is still returned for callers that only want the root.
//
// The reconstruction never mutates the live tree: affected twigs are recomputed
// on scratch copies, the upper tree on a scratch heap.
func (t *Tree) ProofAt(keyHash Hash, undos []*BlockUndo) (proof *Proof, root Hash, found bool, err error) {
	if len(undos) == 0 {
		return nil, Hash{}, false, errors.New("qmdb: ProofAt needs at least one undo record (use GetProof for latest)")
	}
	for _, u := range undos {
		if u == nil {
			return nil, Hash{}, false, errors.New("qmdb: nil undo record")
		}
		if u.broken {
			return nil, Hash{}, false, errors.New("qmdb: undo record is poisoned (entry log / index mismatch at record time)")
		}
	}
	// Sanity: each block's PrevNextSlot must not exceed the next one's (the
	// cursor only grows), and the newest must not exceed the current cursor.
	for i := 1; i < len(undos); i++ {
		if undos[i-1].PrevNextSlot > undos[i].PrevNextSlot {
			return nil, Hash{}, false, errors.New("qmdb: undo records out of order")
		}
	}
	if undos[len(undos)-1].PrevNextSlot > t.nextSlot {
		return nil, Hash{}, false, errors.New("qmdb: undo records are ahead of this tree")
	}

	t.Root() // fold the live tree clean: twig roots + upper are current below

	targetNext := undos[0].PrevNextSlot
	if targetNext == 0 {
		return nil, nullHash, false, nil // empty tree at target
	}

	// Leaves restored at the target: every in-window deactivation of a slot that
	// already existed at the target. (Slots created in-window are null at the
	// target via cursor truncation, even if also deactivated in-window.)
	restored := make(map[uint64]*UndoEntry)
	for _, u := range undos {
		for i := range u.Entries {
			e := &u.Entries[i]
			if e.Slot < targetNext {
				restored[e.Slot] = e
			}
		}
	}

	// Resolve the target key's slot/value at the target state.
	var tSlot uint64
	var tVal []byte
	for s, e := range restored {
		if e.KeyHash == keyHash {
			tSlot, tVal, found = s, e.Value, true
			break
		}
	}
	if !found {
		if s, ok := t.idx.Get(keyHash); ok && s < targetNext {
			e, ok2 := t.entryAt(s)
			if !ok2 || !e.active {
				return nil, Hash{}, false, fmt.Errorf("qmdb: live index points at unreadable slot %d", s)
			}
			tSlot, tVal, found = s, e.value, true
		}
	}

	// Twig layout at the target.
	twigCount := int((targetNext-1)/TwigSize) + 1
	boundaryTwig := int(targetNext / TwigSize) // == twigCount when the tail twig was exactly full

	// SPLIT COMMITMENT: a twig's LEAF tree is frozen — identical at the target
	// and now — for every twig except the boundary twig (in-window appends grew
	// its leaf tree past the target cursor). Everything else that changed
	// in-window is bits-only: restored slots flip their bit back to 1, slots
	// appended in-window flip to 0. So reconstruction hydrates AT MOST two
	// twigs (target + boundary); other affected twigs recombine
	// hashNode(retainedLeafRoot, bitsRoot(targetBits)) with zero rehashing.
	affected := make(map[int]bool)
	for s := range restored {
		affected[int(s/TwigSize)] = true
	}
	if boundaryTwig < twigCount {
		affected[boundaryTwig] = true
	}
	targetTwig := -1
	if found {
		targetTwig = int(tSlot / TwigSize)
		affected[targetTwig] = true
	}

	// bitsAt reconstructs a twig's activeBits at the target.
	bitsAt := func(id int) [TwigSize / 8]byte {
		bits := t.twigs[id].bits
		lo := uint64(id) * TwigSize
		for local := uint64(0); local < TwigSize; local++ {
			s := lo + local
			if s >= targetNext {
				bits[local/8] &^= 1 << uint(local%8) // not appended yet at target
			} else if _, ok := restored[s]; ok {
				bits[local/8] |= 1 << uint(local%8) // died in-window ⇒ live at target
			}
		}
		return bits
	}

	upCapT := 1
	for upCapT < twigCount {
		upCapT <<= 1
	}
	rootsT := make([]Hash, upCapT) // zero = nullHash padding, matching rebuildUpper
	for i := 0; i < twigCount; i++ {
		rootsT[i] = t.twigs[i].root
	}
	var targetNodes *[2 * TwigSize]Hash
	var targetBits [TwigSize / 8]byte
	for id := range affected {
		bits := bitsAt(id)
		var leafRoot Hash
		if id == boundaryTwig || id == targetTwig {
			// Need the leaf NODES (path siblings / target-cursor truncation).
			nodes, err := t.scratchLeafTreeAt(id, targetNext)
			if err != nil {
				return nil, Hash{}, false, err
			}
			leafRoot = nodes[1]
			if id == targetTwig {
				targetNodes = nodes
				targetBits = bits
			}
		} else {
			leafRoot = t.twigs[id].leafRoot // frozen: identical at the target
		}
		b := bits
		rootsT[id] = hashNode(leafRoot, hashBits(&b))
	}

	// Upper tree at the target on a scratch heap (the live heap may differ in
	// both content and capacity if twigs were added in-window).
	upper := make([]Hash, 2*upCapT)
	copy(upper[upCapT:], rootsT)
	for j := upCapT - 1; j >= 1; j-- {
		upper[j] = hashNode(upper[2*j], upper[2*j+1])
	}
	root = upper[1]

	if !found {
		return nil, root, false, nil
	}

	p := &Proof{KeyHash: keyHash, Slot: tSlot, Bits: targetBits}
	p.Value = make([]byte, len(tVal))
	copy(p.Value, tVal)
	j := TwigSize + tSlot%TwigSize
	for L := 0; L < TwigHeight; L++ {
		p.TwigPath[L] = targetNodes[j^1]
		j >>= 1
	}
	j = uint64(upCapT + targetTwig)
	for j > 1 {
		p.UpperPath = append(p.UpperPath, upper[j^1])
		j >>= 1
	}
	return p, root, true, nil
}

// scratchLeafTreeAt rebuilds twig id's FROZEN leaf tree as of the target
// cursor: the live leaf array with slots >= targetNext nulled (they had not
// been appended yet). Deactivations never touch leaves under the split
// commitment, so no restoration is needed. Never mutates the live twig.
func (t *Tree) scratchLeafTreeAt(id int, targetNext uint64) (*[2 * TwigSize]Hash, error) {
	if id >= len(t.twigs) {
		return nil, fmt.Errorf("qmdb: twig %d out of range", id)
	}
	tw := t.twigs[id]
	nodes := new([2 * TwigSize]Hash)
	if tw.pruned {
		// Node storage is gone; the frozen leaves live in the persisted leaf
		// blob, or — when no store is wired (all-in-RAM trees) — the retained
		// entry records (markPruned keeps them in that case).
		hydrated := false
		if t.leafStore != nil {
			if blob, ok := t.leafStore.Leaves(id); ok && len(blob) == TwigSize*32 {
				for i := 0; i < TwigSize; i++ {
					copy(nodes[TwigSize+i][:], blob[i*32:(i+1)*32])
				}
				hydrated = true
			}
		}
		if !hydrated {
			// Last resort: rebuild from entry records. A pruned twig is fully
			// appended, so EVERY slot must yield a record — a freed/absent one
			// means the frozen leaves are unrecoverable: fail loudly.
			lo := uint64(id) * TwigSize
			for local := uint64(0); local < TwigSize; local++ {
				s := lo + local
				if s >= t.nextSlot {
					break
				}
				e, ok := t.entryAt(s)
				if !ok || (e.keyHash == (Hash{}) && e.value == nil) {
					return nil, fmt.Errorf("qmdb: twig %d is pruned and its frozen leaves are unrecoverable (no leaf blob, entry record for slot %d freed)", id, s)
				}
				nodes[TwigSize+local] = hashLeaf(e.keyHash, e.value)
			}
		}
	} else {
		if err := t.ensureHydrated(id); err != nil {
			return nil, err
		}
		if tw.nodes == nil {
			return nil, fmt.Errorf("qmdb: twig %d could not be hydrated", id)
		}
		copy(nodes[TwigSize:], tw.nodes[TwigSize:])
	}
	lo := uint64(id) * TwigSize
	for local := uint64(0); local < TwigSize; local++ {
		if lo+local >= targetNext {
			nodes[TwigSize+local] = nullHash
		}
	}
	for j := TwigSize - 1; j >= 1; j-- {
		nodes[j] = hashNode(nodes[2*j], nodes[2*j+1])
	}
	return nodes, nil
}
