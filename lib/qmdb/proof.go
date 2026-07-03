// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Membership proofs and position-preserving rebuild for the QMDB-twig prototype.

package qmdb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Proof is a membership proof that (KeyHash -> Value) is a live entry committed
// in the world root. It folds the leaf up its twig's FROZEN leaf tree
// (TwigHeight siblings) to the leaf root, combines with the activeBits
// commitment — the verifier checks the slot's bit is SET in the included
// bitmap — and then folds the twig root up the upper tree.
type Proof struct {
	KeyHash   Hash
	Value     []byte
	Slot      uint64
	TwigPath  [TwigHeight]Hash
	Bits      [TwigSize / 8]byte // the twig's activeBits; liveness = Bits[local]
	UpperPath []Hash
}

// GetProof produces a membership proof for a live key, or false if absent.
func (t *Tree) GetProof(keyHash Hash) (*Proof, bool) {
	slot, ok := t.idx.Get(keyHash)
	if !ok {
		return nil, false
	}
	t.Root() // ensure twig/upper roots are current

	twigID := slot / TwigSize
	local := slot % TwigSize
	t.ensureHydrated(int(twigID)) // rebuild nodes if this twig was evicted
	tw := t.twigs[twigID]

	pe, _ := t.entryAt(slot) // faults from cold if the slot was evicted
	p := &Proof{KeyHash: keyHash, Value: pe.value, Slot: slot}

	// Collect siblings inside the twig — read straight from the resident node
	// heap (kept current by setLeaf / recompute), no re-hashing needed.
	j := TwigSize + local
	for L := 0; L < TwigHeight; L++ {
		p.TwigPath[L] = tw.nodes[j^1]
		j >>= 1
	}
	p.Bits = tw.bits

	// Collect siblings up the upper tree — read straight from the incremental
	// upper heap (kept current by Root(), called above), no re-hashing needed.
	j = uint64(t.upCap) + twigID
	for j > 1 {
		p.UpperPath = append(p.UpperPath, t.upper[j^1])
		j >>= 1
	}
	return p, true
}

// VerifyProof checks a membership proof against a world root: the leaf folds to
// the frozen leaf root, the included bitmap must have the slot's bit SET (the
// liveness claim) and hash to the bits commitment, and the combined twig root
// folds up the upper tree.
func VerifyProof(root Hash, p *Proof) bool {
	local := p.Slot % TwigSize
	if p.Bits[local/8]&(1<<uint(local%8)) == 0 {
		return false // slot not live in the presented bitmap
	}
	node := hashLeaf(p.KeyHash, p.Value)
	idx := int(local)
	for L := 0; L < TwigHeight; L++ {
		if idx&1 == 0 {
			node = hashNode(node, p.TwigPath[L])
		} else {
			node = hashNode(p.TwigPath[L], node)
		}
		idx >>= 1
	}
	bits := p.Bits
	node = hashNode(node, hashBits(&bits))
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

// proofCodecVersion tags the wire encoding so consumers can detect a QMDB proof
// (vs an MPT/JMT proof) and route to VerifyEncodedProof. v2 = split twig
// commitment (adds the 256-byte activeBits bitmap after the twig path).
const proofCodecVersion = 0x02

// Marshal encodes a membership proof to a self-describing byte blob:
//
//	[0]        version (0x02)
//	[1..33]    KeyHash (32B)
//	[33..41]   Slot (8B LE)
//	[41]       TwigHeight (1B, sanity)
//	           TwigPath: TwigHeight × 32B
//	[..]       Bits: TwigSize/8 bytes (activeBits bitmap)
//	[..]       upperLen (1B)
//	           UpperPath: upperLen × 32B
//	[..]       valueLen (4B LE) + Value
func (p *Proof) Marshal() []byte {
	out := make([]byte, 0, 1+32+8+1+TwigHeight*32+len(p.Bits)+1+len(p.UpperPath)*32+4+len(p.Value))
	out = append(out, proofCodecVersion)
	out = append(out, p.KeyHash[:]...)
	var slot [8]byte
	binary.LittleEndian.PutUint64(slot[:], p.Slot)
	out = append(out, slot[:]...)
	out = append(out, byte(TwigHeight))
	for i := 0; i < TwigHeight; i++ {
		out = append(out, p.TwigPath[i][:]...)
	}
	out = append(out, p.Bits[:]...)
	out = append(out, byte(len(p.UpperPath)))
	for _, h := range p.UpperPath {
		out = append(out, h[:]...)
	}
	var vl [4]byte
	binary.LittleEndian.PutUint32(vl[:], uint32(len(p.Value)))
	out = append(out, vl[:]...)
	out = append(out, p.Value...)
	return out
}

// UnmarshalProof decodes a blob produced by Proof.Marshal.
func UnmarshalProof(b []byte) (*Proof, error) {
	if len(b) < 1+32+8+1 || b[0] != proofCodecVersion {
		return nil, errors.New("qmdb: not a v1 proof blob")
	}
	p := &Proof{}
	pos := 1
	copy(p.KeyHash[:], b[pos:pos+32])
	pos += 32
	p.Slot = binary.LittleEndian.Uint64(b[pos : pos+8])
	pos += 8
	th := int(b[pos])
	pos++
	if th != TwigHeight {
		return nil, fmt.Errorf("qmdb: twig height %d != %d", th, TwigHeight)
	}
	if len(b) < pos+TwigHeight*32+TwigSize/8+1 {
		return nil, errors.New("qmdb: proof truncated in twig path")
	}
	for i := 0; i < TwigHeight; i++ {
		copy(p.TwigPath[i][:], b[pos:pos+32])
		pos += 32
	}
	copy(p.Bits[:], b[pos:pos+TwigSize/8])
	pos += TwigSize / 8
	ul := int(b[pos])
	pos++
	if len(b) < pos+ul*32+4 {
		return nil, errors.New("qmdb: proof truncated in upper path")
	}
	p.UpperPath = make([]Hash, ul)
	for i := 0; i < ul; i++ {
		copy(p.UpperPath[i][:], b[pos:pos+32])
		pos += 32
	}
	vl := int(binary.LittleEndian.Uint32(b[pos : pos+4]))
	pos += 4
	if len(b) < pos+vl {
		return nil, errors.New("qmdb: proof truncated in value")
	}
	p.Value = make([]byte, vl)
	copy(p.Value, b[pos:pos+vl])
	return p, nil
}

// VerifyEncodedProof decodes a marshaled proof and verifies it against a world
// root — the client-side check for QMDB-native eth_getProof data.
func VerifyEncodedProof(root Hash, blob []byte) bool {
	p, err := UnmarshalProof(blob)
	if err != nil {
		return false
	}
	return VerifyProof(root, p)
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
		// Bulk load: write raw leaves and mark dirty; Root() does one full
		// recompute per twig instead of an O(log) fold per leaf. Under the split
		// commitment DEAD slots keep their entry-hash leaf too — only the bit
		// distinguishes them.
		tw.nodes[TwigSize+local] = hashLeaf(se.KeyHash, v)
		if se.Active {
			tw.setBit(local, true)
			tw.live++
			t.idx.Put(se.KeyHash, se.Slot)
		}
		t.markTwigDirty(tw)
	}
	t.rootDirty = true
	return t
}
