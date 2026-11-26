// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package syncwire

import (
	"errors"
	"fmt"

	"lukechampine.com/blake3"
)

// The QMDB hashing of §1.5, restated here so the state-sync verifier has no
// dependency on the tree implementation. Tests pin it against lib/qmdb.

// KeyHash is BLAKE3-256(preimage): an account key (20-byte address) or a
// storage key (address ‖ slot, 52 bytes).
func KeyHash(preimage []byte) Hash { return blake3.Sum256(preimage) }

// LeafHash is BLAKE3-256(0x01 ‖ keyHash ‖ value).
func LeafHash(keyHash Hash, value []byte) Hash {
	h := blake3.New(32, nil)
	_, _ = h.Write([]byte{0x01})
	_, _ = h.Write(keyHash[:])
	_, _ = h.Write(value)
	var out Hash
	copy(out[:], h.Sum(nil))
	return out
}

// NodeHash is BLAKE3-256(left ‖ right), no domain byte.
func NodeHash(l, r Hash) Hash {
	var buf [64]byte
	copy(buf[:32], l[:])
	copy(buf[32:], r[:])
	return blake3.Sum256(buf[:])
}

// BitsRoot is BLAKE3-256(0x03 ‖ bits).
func BitsRoot(bits *[TwigBitsLen]byte) Hash {
	var buf [1 + TwigBitsLen]byte
	buf[0] = 0x03
	copy(buf[1:], bits[:])
	return blake3.Sum256(buf[:])
}

// TwigRoot is node(leafRoot, bitsRoot).
func TwigRoot(t TwigRoots) Hash { return NodeHash(t.LeafRoot, t.BitsRoot) }

// FoldTwigLeaves folds up to 2048 leaf hashes (missing ones are null, 32
// zero bytes) over the 11 levels of a twig.
func FoldTwigLeaves(leaves []Hash) Hash {
	level := make([]Hash, TwigSize)
	copy(level, leaves)
	for w := TwigSize; w > 1; w /= 2 {
		for i := 0; i < w/2; i++ {
			level[i] = NodeHash(level[2*i], level[2*i+1])
		}
	}
	return level[0]
}

// UpperWidth is next_pow2(twigCount) (1 for 0 or 1).
func UpperWidth(twigCount uint64) uint64 {
	w := uint64(1)
	for w < twigCount {
		w <<= 1
	}
	return w
}

// UpperRoot folds twig roots padded with zero leaves to the next power of
// two; one twig is its own root; no twig is zero.
func UpperRoot(twigRoots []Hash) Hash {
	if len(twigRoots) == 0 {
		return Hash{}
	}
	w := UpperWidth(uint64(len(twigRoots)))
	level := make([]Hash, w)
	copy(level, twigRoots)
	for ; w > 1; w /= 2 {
		for i := uint64(0); i < w/2; i++ {
			level[i] = NodeHash(level[2*i], level[2*i+1])
		}
	}
	return level[0]
}

// RangeProof returns the manifest range proof for leaves [first, first+n)
// of the upper tree over twigRoots: bottom-up, at each level the left
// boundary's outside sibling (if any) before the right boundary's.
func RangeProof(twigRoots []Hash, first, n uint64) ([]Hash, error) {
	total := uint64(len(twigRoots))
	if n == 0 || first >= total || n > total-first {
		return nil, fmt.Errorf("syncwire: range [%d,+%d) outside %d twigs", first, n, total)
	}
	w := UpperWidth(total)
	level := make([]Hash, w)
	copy(level, twigRoots)
	lo, hi := first, first+n-1 // inclusive
	var proof []Hash
	for ; w > 1; w /= 2 {
		if lo%2 == 1 {
			proof = append(proof, level[lo-1])
		}
		if hi%2 == 0 {
			proof = append(proof, level[hi+1])
		}
		next := make([]Hash, w/2)
		for i := uint64(0); i < w/2; i++ {
			next[i] = NodeHash(level[2*i], level[2*i+1])
		}
		level = next
		lo, hi = lo/2, hi/2
	}
	return proof, nil
}

// ErrProof reports a manifest page whose twigs do not fold to its root.
var ErrProof = errors.New("syncwire: manifest range proof does not verify")

// VerifyRange folds the twig roots of [first, first+len(roots)) with proof
// (RangeProof's order) and compares with root. Every proof node must be used.
func VerifyRange(root Hash, twigCount, first uint64, roots []Hash, proof []Hash) error {
	n := uint64(len(roots))
	if n == 0 || first >= twigCount || n > twigCount-first {
		return fmt.Errorf("syncwire: range [%d,+%d) outside %d twigs", first, n, twigCount)
	}
	w := UpperWidth(twigCount)
	lo, hi := first, first+n-1
	cur := append([]Hash(nil), roots...) // cur[i] is node lo+i of this level
	used := 0
	take := func() (Hash, bool) {
		if used >= len(proof) {
			return Hash{}, false
		}
		used++
		return proof[used-1], true
	}
	for ; w > 1; w /= 2 {
		if lo%2 == 1 {
			s, ok := take()
			if !ok {
				return ErrProof
			}
			cur = append([]Hash{s}, cur...)
			lo--
		}
		if hi%2 == 0 {
			s, ok := take()
			if !ok {
				return ErrProof
			}
			cur = append(cur, s)
			hi++
		}
		next := make([]Hash, len(cur)/2)
		for i := range next {
			next[i] = NodeHash(cur[2*i], cur[2*i+1])
		}
		cur, lo, hi = next, lo/2, hi/2
	}
	if used != len(proof) || len(cur) != 1 || cur[0] != root {
		return ErrProof
	}
	return nil
}

// VerifyManifestPage checks a page against the anchored root and the twig
// count implied by next_slot.
func VerifyManifestPage(p ManifestPage, wantRoot Hash) error {
	if p.Root != wantRoot {
		return fmt.Errorf("syncwire: manifest root %x is not the anchored root %x", p.Root, wantRoot)
	}
	if want := (p.NextSlot + TwigSize - 1) / TwigSize; p.TwigCount != want {
		return fmt.Errorf("syncwire: twig_count %d for next_slot %d (want %d)", p.TwigCount, p.NextSlot, want)
	}
	roots := make([]Hash, len(p.Twigs))
	for i, t := range p.Twigs {
		roots[i] = TwigRoot(t)
	}
	if p.TwigCount == 0 {
		if len(p.Twigs) != 0 || len(p.Proof) != 0 || p.Root != (Hash{}) {
			return ErrProof
		}
		return nil
	}
	return VerifyRange(p.Root, p.TwigCount, p.FirstTwig, roots, p.Proof)
}

// TwigSlots returns the slot range [start, end) a chunk for twig id covers
// at a pivot with nextSlot, requested from fromSlot (end <= start: none).
func TwigSlots(id, nextSlot, fromSlot uint64) (start, end uint64) {
	base := id * TwigSize
	start, end = base, base+TwigSize
	if fromSlot > start {
		start = fromSlot
	}
	if nextSlot < end {
		end = nextSlot
	}
	return start, end
}

// VerifyTwig checks one full twig chunk (records from the twig's first
// slot) against its manifest entry and returns the slot leaves: the records
// fold to leaf_root with null padding to the twig's end, bits hash to
// bits_root, and the tag rule holds (LIVE/LIVE_P iff the slot's bit is set;
// HOLLOW only as the sole record of a full all-dead twig). It also returns
// the decoded records in slot order for the caller (nil for HOLLOW).
func VerifyTwig(c TwigChunk, want TwigRoots, nextSlot uint64) error {
	if BitsRoot(&c.Bits) != want.BitsRoot {
		return fmt.Errorf("syncwire: twig %d bits do not hash to bits_root", c.TwigID)
	}
	start, end := TwigSlots(c.TwigID, nextSlot, 0)
	if end <= start {
		return fmt.Errorf("syncwire: twig %d is past next_slot %d", c.TwigID, nextSlot)
	}
	if len(c.Records) == 1 && c.Records[0].Kind == KindHollow {
		if end-start != TwigSize {
			return fmt.Errorf("syncwire: hollow record for the partial twig %d", c.TwigID)
		}
		for _, b := range c.Bits {
			if b != 0 {
				return fmt.Errorf("syncwire: hollow record for twig %d with live bits", c.TwigID)
			}
		}
		if c.Records[0].LeafRoot != want.LeafRoot {
			return fmt.Errorf("syncwire: twig %d hollow leaf_root mismatch", c.TwigID)
		}
		return nil
	}
	if uint64(len(c.Records)) != end-start {
		return fmt.Errorf("syncwire: twig %d carries %d records for %d slots", c.TwigID, len(c.Records), end-start)
	}
	leaves := make([]Hash, end-start)
	for i, r := range c.Records {
		local := i
		live := c.Bits[local/8]&(1<<(local%8)) != 0
		switch r.Kind {
		case KindLive, KindLiveP:
			if !live {
				return fmt.Errorf("syncwire: twig %d slot %d LIVE with a clear bit", c.TwigID, local)
			}
			leaves[i] = LeafHash(r.KeyHash, r.Value)
		case KindDead:
			if live {
				return fmt.Errorf("syncwire: twig %d slot %d DEAD with a set bit", c.TwigID, local)
			}
			leaves[i] = r.LeafHash
		default:
			return fmt.Errorf("syncwire: twig %d slot %d record kind %d", c.TwigID, local, r.Kind)
		}
	}
	// Bits beyond the appended slots must be clear.
	for local := end - start; local < TwigSize; local++ {
		if c.Bits[local/8]&(1<<(local%8)) != 0 {
			return fmt.Errorf("syncwire: twig %d bit %d set past next_slot", c.TwigID, local)
		}
	}
	if FoldTwigLeaves(leaves) != want.LeafRoot {
		return fmt.Errorf("syncwire: twig %d records do not fold to leaf_root", c.TwigID)
	}
	return nil
}
