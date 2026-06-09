// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Membership proofs and position-preserving rebuild for the QMDB-twig prototype.

package qmdb

// Proof is a membership proof that (KeyHash -> Value) is a live entry committed
// in the world root. It folds the leaf up its twig (TwigHeight siblings) and then
// the twig root up the upper tree (one sibling per upper level).
type Proof struct {
	KeyHash   Hash
	Value     []byte
	Slot      uint64
	TwigPath  [TwigHeight]Hash
	UpperPath []Hash
}

// GetProof produces a membership proof for a live key, or false if absent.
func (t *Tree) GetProof(keyHash Hash) (*Proof, bool) {
	slot, ok := t.index[keyHash]
	if !ok {
		return nil, false
	}
	t.Root() // ensure twig/upper roots are current

	twigID := slot / TwigSize
	local := int(slot % TwigSize)
	t.ensureHydrated(int(twigID)) // rebuild leaves if this twig was evicted
	tw := t.twigs[twigID]

	pe, _ := t.entryAt(slot) // faults from cold if the slot was evicted
	p := &Proof{KeyHash: keyHash, Value: pe.value, Slot: slot}

	// Collect siblings inside the twig.
	var buf [TwigSize]Hash
	buf = *tw.leaves
	idx, n := local, TwigSize
	for L := 0; L < TwigHeight; L++ {
		p.TwigPath[L] = buf[idx^1]
		for i := 0; i < n/2; i++ {
			buf[i] = hashNode(buf[2*i], buf[2*i+1])
		}
		n /= 2
		idx >>= 1
	}

	// Collect siblings up the upper tree (padded to pow2).
	roots := make([]Hash, len(t.twigs))
	for i, tw := range t.twigs {
		roots[i] = tw.root
	}
	np := 1
	for np < len(roots) {
		np <<= 1
	}
	ulevel := make([]Hash, np)
	for i := range ulevel {
		if i < len(roots) {
			ulevel[i] = roots[i]
		} else {
			ulevel[i] = nullHash
		}
	}
	uidx, un := int(twigID), np
	for un > 1 {
		p.UpperPath = append(p.UpperPath, ulevel[uidx^1])
		for i := 0; i < un/2; i++ {
			ulevel[i] = hashNode(ulevel[2*i], ulevel[2*i+1])
		}
		un /= 2
		uidx >>= 1
	}
	return p, true
}

// VerifyProof checks a membership proof against a world root. A dead/empty slot
// commits nullHash, which will not equal hashLeaf(K,V), so a valid proof also
// proves the entry is live.
func VerifyProof(root Hash, p *Proof) bool {
	node := hashLeaf(p.KeyHash, p.Value)
	idx := int(p.Slot % TwigSize)
	for L := 0; L < TwigHeight; L++ {
		if idx&1 == 0 {
			node = hashNode(node, p.TwigPath[L])
		} else {
			node = hashNode(p.TwigPath[L], node)
		}
		idx >>= 1
	}
	twigID := int(p.Slot / TwigSize)
	for L := 0; L < len(p.UpperPath); L++ {
		if twigID&1 == 0 {
			node = hashNode(node, p.UpperPath[L])
		} else {
			node = hashNode(p.UpperPath[L], node)
		}
		twigID >>= 1
	}
	return node == root
}

// SlotEntry is one occupied slot in the twig log (for snapshot export/import).
type SlotEntry struct {
	Slot    uint64
	KeyHash Hash
	Value   []byte
	Active  bool
}

// SnapshotLog exports every occupied slot in slot order. This is what a QMDB
// snapshot ships (positions preserved), as opposed to just the live key set.
func (t *Tree) SnapshotLog() []SlotEntry {
	out := make([]SlotEntry, 0, len(t.entries))
	for s := uint64(0); s < t.nextSlot; s++ {
		e, ok := t.entryAt(s)
		if !ok {
			continue
		}
		if e.value == nil && !e.active {
			// truly empty slot (never written) — skip; only export written ones
			if e.keyHash == (Hash{}) {
				continue
			}
		}
		out = append(out, SlotEntry{Slot: s, KeyHash: e.keyHash, Value: e.value, Active: e.active})
	}
	return out
}

// FromSnapshotLog rebuilds a tree from a slot log, preserving exact positions.
// The reconstructed root must equal the original (the position-preserving
// snapshot-sync property). live entries set their leaf; dead slots stay null but
// still consume their slot so later positions match.
func FromSnapshotLog(log []SlotEntry) *Tree {
	t := New()
	maxSlot := uint64(0)
	for _, se := range log {
		if se.Slot+1 > maxSlot {
			maxSlot = se.Slot + 1
		}
	}
	t.nextSlot = maxSlot
	for uint64(len(t.entries)) < maxSlot {
		t.entries = append(t.entries, entry{})
	}
	for _, se := range log {
		tw := t.twigFor(se.Slot)
		local := se.Slot % TwigSize
		v := make([]byte, len(se.Value))
		copy(v, se.Value)
		t.entries[se.Slot] = entry{keyHash: se.KeyHash, value: v, active: se.Active}
		if se.Active {
			tw.leaves[local] = hashLeaf(se.KeyHash, v)
			tw.live++
			t.index[se.KeyHash] = se.Slot
		}
		tw.dirty = true
	}
	t.rootDirty = true
	return t
}
