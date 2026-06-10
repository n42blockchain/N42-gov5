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
	// Recompute dirty twig roots FIRST so the per-twig meta below persists the
	// CURRENT root (resume's fast path trusts these stored roots). Without this a
	// dirty twig would flush a stale root.
	root := t.Root()
	// Append new immutable entries [flushedThrough, nextSlot). These are always
	// in the resident window (eviction only drops already-flushed slots).
	for s := flushedThrough; s < t.nextSlot; s++ {
		e, _ := t.entryAt(s)
		val := make([]byte, 0, 32+len(e.value))
		val = append(val, e.keyHash[:]...)
		val = append(val, e.value...)
		if err := p.Put(EntryTable, be8(s), val); err != nil {
			return flushedThrough, bytesW, err
		}
		bytesW += 8 + len(val)
	}
	// Twig metadata: root + activeBits (256 bytes for 2048 slots). Skip twigs
	// whose leaves were EVICTED (leaves==nil, not pruned): their meta was already
	// persisted before eviction and an evicted twig is, by construction, unchanged
	// since (any mutation rehydrates it). Pruned twigs (leaves==nil, pruned) still
	// flush their constant null-twig meta.
	for id, tw := range t.twigs {
		if tw == nil {
			continue
		}
		if tw.leaves == nil && !tw.pruned {
			continue // evicted-clean: on-disk meta is current
		}
		meta := make([]byte, 32+TwigSize/8)
		copy(meta[:32], tw.root[:])
		// activeBits from live leaves (non-null = active in this prototype).
		if !tw.pruned && tw.leaves != nil {
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
//
// Memory-bounded reload: only twig leaves + the live-key index + nextSlot are
// materialized; the entry RECORDS are NOT retained (the resident window starts
// empty at entriesBase=nextSlot). Live values are served from the cold reader,
// which LoadFrom attaches to g itself — so Get/GetProof work immediately after a
// load without the caller holding the whole entry log in RAM.
func (t *Tree) LoadFrom(g Getter) error {
	nsRaw, err := g.GetOne(MetaTable, []byte("nextSlot"))
	if err != nil {
		return err
	}
	// Serve live values from the persisted log even if nothing is loaded yet.
	if t.cold == nil {
		t.cold = ColdReaderFromGetter(g)
	}
	if len(nsRaw) < 8 {
		return nil // nothing persisted yet
	}
	nextSlot := binary.BigEndian.Uint64(nsRaw)
	numTwigs := int((nextSlot + TwigSize - 1) / TwigSize)
	activeTwig := int((nextSlot - 1) / TwigSize) // twig holding the last live slot

	t.twigs = make([]*twig, numTwigs)
	// A persistent index (MDBX-backed) already holds the live key set across
	// restarts, so it must NOT be rescanned; only a fresh in-RAM index (Len()==0)
	// needs rebuilding from the entry log.
	rebuildIndex := t.idx.Len() == 0

	// Build twig-by-twig and evict each sealed twig's leaves as soon as its root is
	// computed, so at most one twig's leaves (+ the active twig) are resident at a
	// time — the reload itself is memory-bounded, not O(history).
	for id := 0; id < numTwigs; id++ {
		tw := newTwig()
		t.twigs[id] = tw
		meta, e := g.GetOne(TwigTable, be8(uint64(id)))
		if e != nil {
			return e
		}
		var activeBits []byte
		var storedRoot Hash
		if len(meta) >= 32+TwigSize/8 {
			copy(storedRoot[:], meta[:32])
			activeBits = meta[32 : 32+TwigSize/8]
		}
		// Fast path: a sealed twig with a persistent index trusts its stored root
		// and skips the per-slot entry scan entirely — O(numTwigs) resume, O(1) RAM.
		if !rebuildIndex && id < activeTwig {
			tw.root = storedRoot
			tw.dirty = false
			tw.leaves = nil // sealed: no leaves resident
			// recover live count from activeBits for compaction sparsity decisions
			if activeBits != nil {
				cnt := 0
				lim := TwigSize
				if rem := int(nextSlot - uint64(id)*TwigSize); rem < lim {
					lim = rem
				}
				for local := 0; local < lim; local++ {
					if activeBits[local/8]&(1<<uint(local%8)) != 0 {
						cnt++
					}
				}
				tw.live = cnt
			}
			continue
		}
		if activeBits != nil {
			lo := uint64(id) * TwigSize
			for local := 0; local < TwigSize; local++ {
				slot := lo + uint64(local)
				if slot >= nextSlot {
					break
				}
				if activeBits[local/8]&(1<<uint(local%8)) == 0 {
					continue
				}
				v, e := g.GetOne(EntryTable, be8(slot))
				if e != nil {
					return e
				}
				if len(v) < 32 {
					continue
				}
				var kh Hash
				copy(kh[:], v[:32])
				tw.leaves[local] = hashLeaf(kh, v[32:])
				tw.live++
				if rebuildIndex {
					t.idx.Put(kh, slot)
				}
			}
		}
		tw.recompute() // set this twig's root from its leaves
		if id < activeTwig {
			tw.leaves = nil // sealed: keep only the 32-byte root, free the array
		}
	}
	// Entry records are not retained — start the resident window empty at the
	// append cursor. Future appends grow it; cold serves everything below.
	t.entries = nil
	t.entriesBase = nextSlot
	t.evicted = nextSlot
	t.nextSlot = nextSlot
	t.rootDirty = true
	t.Root() // materialize twig + world roots
	return nil
}

// OnDiskBytes reports the persistent footprint without flushing: the entry log
// (immutable appends) plus twig metadata. This is the apples-to-apples analogue
// of a Merkle tree's node-store bytes.
func (t *Tree) OnDiskBytes() (entryBytes, twigBytes int) {
	// Resident window only; evicted slots live in the cold store (counted there).
	for i := range t.entries {
		e := t.entries[i]
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
