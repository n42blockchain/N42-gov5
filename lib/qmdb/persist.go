// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Persistence for the QMDB-twig store. Unlike a content-addressed Merkle tree
// (random hash keys -> random B+tree writes), QMDB persists by POSITION:
//   - the entry log is keyed by the global slot (a strictly increasing uint64),
//     so writes are pure sequential appends (MDBX MDB_APPEND-friendly), and
//   - twig metadata (root + activeBits) is keyed by twig id (also sequential).
// The key->slot index is in-memory (rebuilt from the log), matching QMDB's
// ~2.3 B/entry footprint. This file provides the flush + footprint accounting
// used by the benchmark; the MDBX table wiring lives with the root computer.

package qmdb

import "encoding/binary"

// Putter is the minimal sink the tree flushes into (an adapter over kv.RwTx or a
// byte counter for benchmarks).
type Putter interface {
	Put(table string, key, value []byte) error
}

// Getter is the minimal source the tree reloads from (an adapter over kv.Tx).
// GetOne returns nil (not an error) for an absent key.
type Getter interface {
	GetOne(table string, key []byte) ([]byte, error)
}

// Table names for the positional layout.
const (
	EntryTable = "qmdbEntries" // BE8(slot) -> keyHash(32) || value
	TwigTable  = "qmdbTwigs"   // BE8(twigID) -> twigRoot(32) || activeBits(256)
	MetaTable  = "qmdbMeta"    // "root","version","nextSlot"
)

func be8(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

// FlushTo appends entries written since the last flush (sequential), writes dirty
// twig metadata (sequential by id), and the recovery meta. Returns bytes written.
// flushedThrough is the slot cursor already persisted (0 on first flush).
func (t *Tree) FlushTo(p Putter, flushedThrough uint64) (uint64, int, error) {
	bytesW := 0
	// Append new immutable entries [flushedThrough, nextSlot).
	for s := flushedThrough; s < t.nextSlot; s++ {
		e := t.entries[s]
		val := make([]byte, 0, 32+len(e.value))
		val = append(val, e.keyHash[:]...)
		val = append(val, e.value...)
		if err := p.Put(EntryTable, be8(s), val); err != nil {
			return flushedThrough, bytesW, err
		}
		bytesW += 8 + len(val)
	}
	// Dirty twig metadata: root + activeBits (256 bytes for 2048 slots).
	for id, tw := range t.twigs {
		if tw == nil {
			continue
		}
		meta := make([]byte, 32+TwigSize/8)
		copy(meta[:32], tw.root[:])
		// activeBits from live leaves (non-null = active in this prototype).
		if !tw.pruned {
			for slot := 0; slot < TwigSize; slot++ {
				if tw.leaves[slot] != nullHash {
					meta[32+slot/8] |= 1 << uint(slot%8)
				}
			}
		}
		if err := p.Put(TwigTable, be8(uint64(id)), meta); err != nil {
			return flushedThrough, bytesW, err
		}
		bytesW += 8 + len(meta)
	}
	root := t.Root()
	_ = p.Put(MetaTable, []byte("root"), root[:])
	_ = p.Put(MetaTable, []byte("nextSlot"), be8(t.nextSlot))
	bytesW += 32 + 8
	return t.nextSlot, bytesW, nil
}

// LoadFrom rebuilds the in-memory forest from a previously-flushed positional
// layout: the entry log (slot -> keyHash||value) plus per-twig activeBits (which
// slots are live — the source of truth that distinguishes a deleted key's last
// entry from a live one). nextSlot comes from the meta table. Twig roots are
// recomputed rather than trusted, so the reconstructed root is self-consistent.
// A fresh (never-flushed) store leaves the tree empty.
func (t *Tree) LoadFrom(g Getter) error {
	nsRaw, err := g.GetOne(MetaTable, []byte("nextSlot"))
	if err != nil {
		return err
	}
	if len(nsRaw) < 8 {
		return nil // nothing persisted yet
	}
	nextSlot := binary.BigEndian.Uint64(nsRaw)
	numTwigs := int((nextSlot + TwigSize - 1) / TwigSize)

	t.entries = make([]entry, nextSlot)
	t.twigs = make([]*twig, numTwigs)
	t.index = make(map[Hash]uint64, nextSlot)
	for id := 0; id < numTwigs; id++ {
		t.twigs[id] = newTwig()
	}

	// Per-twig activeBits.
	activeBits := make([][]byte, numTwigs)
	for id := 0; id < numTwigs; id++ {
		meta, e := g.GetOne(TwigTable, be8(uint64(id)))
		if e != nil {
			return e
		}
		if len(meta) >= 32+TwigSize/8 {
			activeBits[id] = meta[32 : 32+TwigSize/8]
		}
	}

	for slot := uint64(0); slot < nextSlot; slot++ {
		v, e := g.GetOne(EntryTable, be8(slot))
		if e != nil {
			return e
		}
		if len(v) < 32 {
			continue // pruned/freed slot
		}
		var kh Hash
		copy(kh[:], v[:32])
		val := make([]byte, len(v)-32)
		copy(val, v[32:])
		id := int(slot / TwigSize)
		local := slot % TwigSize
		active := false
		if ab := activeBits[id]; ab != nil {
			active = ab[local/8]&(1<<uint(local%8)) != 0
		}
		t.entries[slot] = entry{keyHash: kh, value: val, active: active}
		if active {
			tw := t.twigs[id]
			tw.leaves[local] = hashLeaf(kh, val)
			tw.live++
			t.index[kh] = slot
		}
	}
	t.nextSlot = nextSlot
	t.rootDirty = true
	t.Root() // materialize twig + world roots
	return nil
}

// OnDiskBytes reports the persistent footprint without flushing: the entry log
// (immutable appends) plus twig metadata. This is the apples-to-apples analogue
// of a Merkle tree's node-store bytes.
func (t *Tree) OnDiskBytes() (entryBytes, twigBytes int) {
	for s := uint64(0); s < t.nextSlot; s++ {
		e := t.entries[s]
		// pruned slots are freed; count only resident records.
		if e.keyHash == (Hash{}) && e.value == nil {
			continue
		}
		entryBytes += 8 + 32 + len(e.value)
	}
	resident := 0
	for _, tw := range t.twigs {
		if tw != nil && !tw.pruned {
			resident++
		}
	}
	twigBytes = resident * (8 + 32 + TwigSize/8)
	return entryBytes, twigBytes
}
