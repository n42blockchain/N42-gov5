// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Full-history proofs over the split twig commitment. The enabling structure:
//
//   - The entry log is append-only and each slot's leaf hash is FROZEN at
//     append time (leaves never change on deactivation).
//   - A slot's liveness bit dies AT MOST ONCE (an overwrite appends a new
//     slot; nothing resurrects an old one).
//
// So the ENTIRE liveness history of the chain is captured by one death stamp
// per slot, and the tree at any height H is a pure function of:
//
//	E(H)      — the append cursor at the end of block H   (8 B per block)
//	death[s]  — the block that killed slot s, or 0=alive   (4 B per slot)
//	the frozen leaf trees (already persisted: leaf blobs / entry rows)
//
//	bits(tw, H)[local] = (twStart+local < E(H)) && (death == 0 || death > H)
//	twigRoot(tw, H)    = hashNode(leafRoot(tw,H), Blake3(0x03‖bits(tw,H)))
//	root(H)            = fold of twigRoot(·,H) over ceil(E(H)/TwigSize) twigs
//
// leafRoot(tw,H) equals the twig's frozen leafRoot for every twig fully
// appended by H; only the boundary twig (containing E(H)) needs its leaf tree
// re-folded with the not-yet-appended tail nulled.
//
// To keep queries sub-second the upper tree is NOT recomputed wholesale:
// shallow upper nodes (the "top band", heap indices [1, topBandNodes)) are
// journaled per block, deeper siblings re-derive on demand by folding the
// (cheap, stamps-driven) twig roots under them. A per-key version index turns
// "value of K at H" into one floor lookup.
//
// The HistoryStore is deliberately a narrow interface (like Putter/Getter/
// LeafStore) so the engine can back it with MDBX tables and tests with maps.

package qmdb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// topBandNodes: upper-heap indices [1, topBandNodes) are journaled per block.
// 64 ⇒ depths 0..5 (1+2+4+8+16+32 = 63 nodes, ≤2 KiB per changed block); the
// deepest recomputed sibling then covers upCap/64 twigs.
const topBandNodes = 64

// aliveStamp marks a never-deactivated slot in the death-stamp blob.
const aliveStamp = uint32(0)

// HistoryStore persists the history layer. All methods are point lookups or
// upserts; Flush batching/atomicity is the implementation's concern (the
// engine writes through the same tx as the block).
type HistoryStore interface {
	// Death stamps, one u32 LE per local slot, TwigSize entries per twig blob.
	// Absent twig ⇒ all alive.
	PutDeathStamps(twigID uint64, blob []byte) error
	GetDeathStamps(twigID uint64) ([]byte, error)

	// E(block): the append cursor at the END of the block.
	PutBlockPos(block, nextSlot uint64) error
	// FloorBlockPos returns E at the newest recorded block ≤ block (blocks with
	// no state change may be unrecorded). ok=false ⇒ no record ≤ block.
	FloorBlockPos(block uint64) (uint64, bool, error)

	// Per-key version positions, ascending.
	AppendKeyVersion(keyHash Hash, pos uint64) error
	KeyVersions(keyHash Hash) ([]uint64, error)

	// Top-band journal: the packed upper[1:topBandNodes) heap prefix, written
	// on blocks whose root changed. FloorTopBand returns the newest record at
	// a block ≤ the argument.
	PutTopBand(block uint64, packed []byte) error
	FloorTopBand(block uint64) ([]byte, bool, error)
}

// HistoryRecorder captures per-block history while blocks execute. Wire with
// Tree.SetHistoryRecorder, then bracket each block with BeginBlock/EndBlock.
type HistoryRecorder struct {
	store HistoryStore
	block uint64

	// stampDelta: twig → local → death block, accumulated since the last
	// EndBlock flush (read-modify-write per touched twig blob).
	stampDelta map[uint64]map[uint64]uint32

	// appendErr latches a key-version sink failure from Set (which has no
	// error return); EndBlock surfaces it.
	appendErr error
}

// NewHistoryRecorder creates a recorder writing through store.
func NewHistoryRecorder(store HistoryStore) *HistoryRecorder {
	return &HistoryRecorder{store: store, stampDelta: make(map[uint64]map[uint64]uint32)}
}

// SetHistoryRecorder attaches (or detaches, nil) the history recorder. While
// attached, dead entry rows are RETAINED at flush (historical reads need the
// values), overriding the leaf-store row elision.
func (t *Tree) SetHistoryRecorder(r *HistoryRecorder) { t.hist = r }

// BeginBlock declares the block number for subsequent operations. Block 0 is
// reserved (aliveStamp); chains record from block 1.
func (t *Tree) BeginBlock(block uint64) error {
	if t.hist == nil {
		return nil
	}
	if block == 0 || block > uint64(^uint32(0)) {
		return fmt.Errorf("qmdb history: block %d out of the stampable range [1, 2^32)", block)
	}
	t.hist.block = block
	return nil
}

// EndBlock settles the tree (Root) and journals the block's history: death
// stamps of slots killed in this block, E(block), the key versions appended,
// and the top band. Call AFTER the block's Set/Delete operations.
func (t *Tree) EndBlock() error {
	if t.hist == nil {
		return nil
	}
	r := t.hist
	if r.block == 0 {
		return errors.New("qmdb history: EndBlock without BeginBlock")
	}
	if r.appendErr != nil {
		err := r.appendErr
		r.appendErr = nil
		return fmt.Errorf("qmdb history: key-version append failed mid-block: %w", err)
	}
	t.Root() // settle twig + upper heaps (also flushes batch folding)

	// Death stamps stay ACCUMULATED in stampDelta and flush once per batch
	// (FlushHistory) — a per-block read-modify-write of each touched twig's
	// 8 KiB blob would be ruinous write amplification in the replay hot loop.
	// Reads before the flush overlay the pending delta (stampsAt).

	if err := r.store.PutBlockPos(r.block, t.nextSlot); err != nil {
		return err
	}

	// Top band: pack [upCap 8B LE] + upper[1:min(topBandNodes, 2*upCap)). The
	// upCap prefix pins the heap LAYOUT the nodes were recorded under — a
	// reader may only use the band when its historical upCap matches (node j
	// covers different twig ranges across doublings).
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
	if err := r.store.PutTopBand(r.block, packed); err != nil {
		return err
	}
	r.block = 0
	return nil
}

// reset drops all pending (not-yet-flushed) death-stamp deltas. Called when the
// tree is reloaded from disk: the pending deltas belong to the discarded
// in-memory tree, not the reloaded one.
func (r *HistoryRecorder) reset() {
	r.stampDelta = make(map[uint64]map[uint64]uint32)
	r.appendErr = nil
}

// recordDeath stamps a slot killed in the current block.
func (r *HistoryRecorder) recordDeath(slot uint64) {
	twigID, local := slot/TwigSize, slot%TwigSize
	m := r.stampDelta[twigID]
	if m == nil {
		m = make(map[uint64]uint32, 4)
		r.stampDelta[twigID] = m
	}
	m[local] = uint32(r.block)
}

// stampsAt returns twigID's death-stamp blob as of NOW: the persisted blob
// overlaid with the batch's pending delta.
func (r *HistoryRecorder) stampsAt(twigID uint64) ([]byte, error) {
	blob, err := r.store.GetDeathStamps(twigID)
	if err != nil {
		return nil, err
	}
	delta := r.stampDelta[twigID]
	if len(delta) == 0 {
		return blob, nil
	}
	merged := make([]byte, TwigSize*4)
	copy(merged, blob)
	for local, death := range delta {
		binary.LittleEndian.PutUint32(merged[local*4:], death)
	}
	return merged, nil
}

// FlushHistory writes the accumulated death-stamp deltas through the store —
// one read-modify-write per touched twig per BATCH. Call right before the
// batch commit (alongside Tree.FlushTo).
func (t *Tree) FlushHistory() error {
	if t.hist == nil {
		return nil
	}
	r := t.hist
	for twigID, locals := range r.stampDelta {
		blob, err := r.store.GetDeathStamps(twigID)
		if err != nil {
			return err
		}
		if len(blob) != TwigSize*4 {
			nb := make([]byte, TwigSize*4)
			copy(nb, blob)
			blob = nb
		} else {
			blob = append([]byte(nil), blob...) // never alias the store's memory
		}
		for local, death := range locals {
			binary.LittleEndian.PutUint32(blob[local*4:], death)
		}
		if err := r.store.PutDeathStamps(twigID, blob); err != nil {
			return err
		}
		delete(r.stampDelta, twigID)
	}
	return nil
}

// historyBitsAt reconstructs a twig's activeBits at height H from its death
// stamps and the append cursor eH.
func historyBitsAt(stamps []byte, twigID, eH, h uint64) [TwigSize / 8]byte {
	var bits [TwigSize / 8]byte
	lo := twigID * TwigSize
	lim := uint64(TwigSize)
	if lo >= eH {
		return bits
	}
	if rem := eH - lo; rem < lim {
		lim = rem
	}
	for local := uint64(0); local < lim; local++ {
		death := aliveStamp
		if len(stamps) >= int(local+1)*4 {
			death = binary.LittleEndian.Uint32(stamps[local*4:])
		}
		if death == aliveStamp || uint64(death) > h {
			bits[local/8] |= 1 << uint(local%8)
		}
	}
	return bits
}

// twigRootAtHeight derives one twig's committed root at height H. For the
// boundary twig (partially appended at H) it re-folds the frozen leaf tree
// with the unappended tail nulled; full twigs reuse the retained leafRoot.
func (t *Tree) twigRootAtHeight(twigID, eH, h uint64, stamps []byte) (Hash, error) {
	bits := historyBitsAt(stamps, twigID, eH, h)
	var leafRoot Hash
	if (twigID+1)*TwigSize <= eH {
		leafRoot = t.twigs[twigID].leafRoot // frozen: identical at H
	} else {
		nodes, err := t.scratchLeafTreeAt(int(twigID), eH)
		if err != nil {
			return Hash{}, err
		}
		leafRoot = nodes[1]
	}
	return hashNode(leafRoot, hashBits(&bits)), nil
}

// ProofAtHeight produces a membership proof for keyHash against the world root
// at the END of block h, from the journaled history. The returned root MUST be
// checked against the trusted header root for block h by the caller.
//
// found=false ⇒ the key had no live entry at h (root still returned).
func (t *Tree) ProofAtHeight(keyHash Hash, h uint64) (proof *Proof, root Hash, found bool, err error) {
	if t.hist == nil {
		return nil, Hash{}, false, errors.New("qmdb history: no recorder/store attached")
	}
	store := t.hist.store
	eH, ok, err := store.FloorBlockPos(h)
	if err != nil {
		return nil, Hash{}, false, err
	}
	if !ok || eH == 0 {
		return nil, nullHash, false, nil // empty tree at h
	}
	if eH > t.nextSlot {
		return nil, Hash{}, false, fmt.Errorf("qmdb history: E(%d)=%d is ahead of this tree (next=%d)", h, eH, t.nextSlot)
	}

	// Resolve the key's live version at h: newest pos < E(h) not yet dead.
	versions, err := store.KeyVersions(keyHash)
	if err != nil {
		return nil, Hash{}, false, err
	}
	var tSlot uint64
	for i := len(versions) - 1; i >= 0; i-- {
		if versions[i] >= eH {
			continue
		}
		tSlot = versions[i]
		stamps, err := t.hist.stampsAt(tSlot / TwigSize)
		if err != nil {
			return nil, Hash{}, false, err
		}
		death := aliveStamp
		if local := tSlot % TwigSize; len(stamps) >= int(local+1)*4 {
			death = binary.LittleEndian.Uint32(stamps[local*4:])
		}
		found = death == aliveStamp || uint64(death) > h
		break // only the newest pre-E(h) version can be live at h
	}

	twigCountH := int((eH-1)/TwigSize) + 1
	upCapH := 1
	for upCapH < twigCountH {
		upCapH <<= 1
	}

	// Sibling values at h, twig-root level upward. Top band from the journal;
	// below-band siblings re-derive by folding their covered twig roots.
	band, bandOK, err := store.FloorTopBand(h)
	if err != nil {
		return nil, Hash{}, false, err
	}
	if !bandOK {
		return nil, Hash{}, false, errors.New("qmdb history: no top-band record at or before h")
	}
	bandUpCap := 0
	if len(band) >= 8 {
		bandUpCap = int(binary.LittleEndian.Uint64(band))
		band = band[8:]
	}
	bandNodes := len(band) / 32
	bandAt := func(j int) (Hash, bool) {
		// Valid only when the band was recorded under the SAME heap layout as
		// the height being reconstructed (upCap pins node→twig-range coverage).
		if bandUpCap == upCapH && j >= 1 && j-1 < bandNodes && j < topBandNodes {
			var v Hash
			copy(v[:], band[(j-1)*32:])
			return v, true
		}
		return Hash{}, false
	}
	// nodeAt derives upper-heap node j (in the upCapH layout) at height h.
	var nodeAt func(j int) (Hash, error)
	nodeAt = func(j int) (Hash, error) {
		if j >= upCapH { // twig-root level
			twigID := uint64(j - upCapH)
			if twigID >= uint64(twigCountH) {
				return nullHash, nil // padding
			}
			stamps, err := t.hist.stampsAt(twigID)
			if err != nil {
				return Hash{}, err
			}
			return t.twigRootAtHeight(twigID, eH, h, stamps)
		}
		if v, ok := bandAt(j); ok {
			return v, nil
		}
		l, err := nodeAt(2 * j)
		if err != nil {
			return Hash{}, err
		}
		rgt, err := nodeAt(2*j + 1)
		if err != nil {
			return Hash{}, err
		}
		return hashNode(l, rgt), nil
	}

	if !found {
		r, err := nodeAt(1)
		if err != nil {
			return nil, Hash{}, false, err
		}
		return nil, r, false, nil
	}

	// Value + leaf path of the target slot.
	e, ok2 := t.entryAt(tSlot)
	if !ok2 || (e.keyHash == (Hash{}) && e.value == nil) {
		return nil, Hash{}, false, fmt.Errorf("qmdb history: entry for slot %d unavailable (archive rows required)", tSlot)
	}
	if e.keyHash != keyHash {
		return nil, Hash{}, false, fmt.Errorf("qmdb history: version index points slot %d at a different key", tSlot)
	}
	targetTwig := tSlot / TwigSize
	var targetNodes *[2 * TwigSize]Hash
	targetNodes, err = t.scratchLeafTreeAt(int(targetTwig), eH)
	if err != nil {
		return nil, Hash{}, false, err
	}
	stamps, err := t.hist.stampsAt(targetTwig)
	if err != nil {
		return nil, Hash{}, false, err
	}

	p := &Proof{KeyHash: keyHash, Slot: tSlot, Bits: historyBitsAt(stamps, targetTwig, eH, h)}
	p.Value = make([]byte, len(e.value))
	copy(p.Value, e.value)
	j := TwigSize + tSlot%TwigSize
	for L := 0; L < TwigHeight; L++ {
		p.TwigPath[L] = targetNodes[j^1]
		j >>= 1
	}
	jj := int(uint64(upCapH) + targetTwig)
	for jj > 1 {
		sib, err := nodeAt(jj ^ 1)
		if err != nil {
			return nil, Hash{}, false, err
		}
		p.UpperPath = append(p.UpperPath, sib)
		jj >>= 1
	}
	r, err := nodeAt(1)
	if err != nil {
		return nil, Hash{}, false, err
	}
	return p, r, true, nil
}
