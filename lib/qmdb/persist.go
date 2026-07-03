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

import (
	"encoding/binary"
	"errors"
)

var (
	// errPreSplitMeta: the on-disk twig metadata predates the split twig
	// commitment (root formula change). There is no in-place migration — the
	// chain must be re-replayed under the new format.
	errPreSplitMeta = errors.New("qmdb: pre-split-commitment twig metadata — re-replay this database under the split twig commitment")
	// errTwigMetaInconsistent: stored root ≠ recomputed/combined root.
	errTwigMetaInconsistent = errors.New("qmdb: twig metadata inconsistent (stored root does not match leafRoot+bits)")
)

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

// Deleter is the optional sink capability used to reclaim dead entry rows
// (kv.RwTx satisfies it). When the Putter passed to FlushTo also implements
// Deleter, rows of slots deactivated after they were flushed are deleted, so
// the on-disk entry log tracks the live set instead of the full history.
type Deleter interface {
	Delete(table string, k []byte) error
}

// Table names for the positional layout.
const (
	EntryTable  = "qmdbEntries"    // BE8(slot) -> keyHash(32) || value
	TwigTable   = "qmdbTwigs"      // BE8(twigID) -> twigRoot(32) || activeBits(256)
	MetaTable   = "qmdbMeta"       // "root","version","nextSlot","liveCount"
	LeavesTable = "qmdbTwigLeaves" // BE8(twigID) -> TwigSize*32 leaf blob (fast rehydrate)
)

// Twig leaf blobs are stored SPARSE: most slots in a sealed twig are null
// (deactivated or never written — ~90% on the converted chain), and the raw
// 64 KiB-per-twig format spent 1.8 GB at 12.7M blocks mostly on zero bytes.
//
//	[0] 0xFE  — sparse marker (a raw legacy blob is exactly TwigSize*32 bytes
//	            and is still accepted on read)
//	[1] 0x01  — version
//	[2:2+TwigSize/8] presence bitmap (bit set = non-null leaf)
//	then the non-null 32 B leaf hashes in slot order.
const (
	sparseLeavesMarker  = 0xFE
	sparseLeavesVersion = 0x01
	leavesBitmapLen     = TwigSize / 8
)

// encodeSparseLeaves packs the non-null leaves of a twig's node heap.
func encodeSparseLeaves(nodes *[2 * TwigSize]Hash) []byte {
	nonNull := 0
	for i := 0; i < TwigSize; i++ {
		if nodes[TwigSize+i] != nullHash {
			nonNull++
		}
	}
	out := make([]byte, 2+leavesBitmapLen, 2+leavesBitmapLen+32*nonNull)
	out[0], out[1] = sparseLeavesMarker, sparseLeavesVersion
	for i := 0; i < TwigSize; i++ {
		if h := nodes[TwigSize+i]; h != nullHash {
			out[2+i/8] |= 1 << uint(i%8)
			out = append(out, h[:]...)
		}
	}
	return out
}

// decodeSparseLeaves expands a sparse blob to the raw TwigSize*32 layout.
// Returns nil if the blob is malformed.
func decodeSparseLeaves(v []byte) []byte {
	if len(v) < 2+leavesBitmapLen || v[0] != sparseLeavesMarker || v[1] != sparseLeavesVersion {
		return nil
	}
	bitmap := v[2 : 2+leavesBitmapLen]
	p := v[2+leavesBitmapLen:]
	out := make([]byte, TwigSize*32)
	for i := 0; i < TwigSize; i++ {
		if bitmap[i/8]&(1<<uint(i%8)) == 0 {
			continue
		}
		if len(p) < 32 {
			return nil // truncated
		}
		copy(out[i*32:(i+1)*32], p[:32])
		p = p[32:]
	}
	if len(p) != 0 {
		return nil // trailing garbage
	}
	return out
}

// leafStoreGetter adapts a Getter into a LeafStore (reads the QMDBTwigLeaves
// blob, accepting both the sparse and the raw legacy format).
type leafStoreGetter struct{ g Getter }

func (l leafStoreGetter) Leaves(id int) ([]byte, bool) {
	v, err := l.g.GetOne(LeavesTable, be8(uint64(id)))
	if err != nil || len(v) == 0 {
		return nil, false
	}
	if len(v) == TwigSize*32 {
		return v, true // raw legacy blob
	}
	if exp := decodeSparseLeaves(v); exp != nil {
		return exp, true
	}
	return nil, false
}

// LeafStoreFromGetter wraps a Getter (kv.Tx / map store) as a LeafStore.
func LeafStoreFromGetter(g Getter) LeafStore { return leafStoreGetter{g} }

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
	// Append new entries [flushedThrough, nextSlot). These are always in the
	// resident window (eviction only drops already-flushed slots). Under the
	// split commitment DEAD slots' frozen leaf hashes stay part of the twig
	// commitment forever, so their rows may be elided ONLY when a leaf store
	// persists the blobs (the alternate leaf-recovery source); a blob-less
	// tree writes every row so a reload can rebuild the frozen leaves.
	for s := flushedThrough; s < t.nextSlot; s++ {
		e, ok := t.entryAt(s)
		if !ok {
			continue
		}
		if !e.active && t.leafStore != nil && t.hist == nil {
			continue // dead at flush; blob carries its frozen leaf (non-archive)
		}
		val := make([]byte, 0, 32+len(e.value))
		val = append(val, e.keyHash[:]...)
		val = append(val, e.value...)
		if err := p.Put(EntryTable, be8(s), val); err != nil {
			return flushedThrough, bytesW, err
		}
		bytesW += 8 + len(val)
	}
	// Reclaim rows of slots that were flushed earlier but have since died —
	// ONLY when a leaf store is wired: the persisted leaf blob then carries the
	// dead slots' frozen hashes, so their rows are never needed again. Without
	// a leaf store the rows are the only leaf-recovery source — keep them.
	if d, ok := p.(Deleter); ok && t.leafStore != nil && t.hist == nil && len(t.deadFlushed) > 0 {
		for _, s := range t.deadFlushed {
			if err := d.Delete(EntryTable, be8(s)); err != nil {
				return flushedThrough, bytesW, err
			}
		}
		t.deadFlushed = t.deadFlushed[:0]
	}
	// Twig metadata v2: root + leafRoot + activeBits. Skip twigs whose leaves
	// were EVICTED (nodes==nil, not pruned) ONLY when their meta is clean —
	// under the split commitment a deactivation flips bits WITHOUT rehydrating,
	// so an evicted twig's meta can go stale (metaDirty tracks this). Pruned
	// twigs still flush their meta.
	for id, tw := range t.twigs {
		if tw == nil {
			continue
		}
		if tw.nodes == nil && !tw.pruned && !tw.metaDirty {
			continue // evicted-clean: on-disk meta is current
		}
		meta := make([]byte, 32+32+TwigSize/8)
		copy(meta[:32], tw.root[:])
		copy(meta[32:64], tw.leafRoot[:])
		copy(meta[64:], tw.bits[:])
		if err := p.Put(TwigTable, be8(uint64(id)), meta); err != nil {
			return flushedThrough, bytesW, err
		}
		bytesW += 8 + len(meta)
		tw.metaDirty = false
		// Persist the leaf blob (one-read rehydration) when a leaf store is in
		// use. Under the split commitment the blob holds every APPENDED slot's
		// entry hash — dead slots included, since the frozen leaf tree commits
		// them forever (sparse encoding still elides never-appended tail slots).
		// Skipped for evicted twigs (nodes==nil): their frozen blob is current.
		if t.leafStore != nil && tw.nodes != nil {
			blob := encodeSparseLeaves(tw.nodes)
			if err := p.Put(LeavesTable, be8(uint64(id)), blob); err != nil {
				return flushedThrough, bytesW, err
			}
			bytesW += 8 + len(blob)
		}
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
		var storedRoot, storedLeafRoot Hash
		haveMeta := false
		switch {
		case len(meta) >= 32+32+TwigSize/8: // v2: root || leafRoot || bits
			copy(storedRoot[:], meta[:32])
			copy(storedLeafRoot[:], meta[32:64])
			copy(tw.bits[:], meta[64:64+TwigSize/8])
			haveMeta = true
		case len(meta) >= 32+TwigSize/8:
			return errPreSplitMeta // v1 (pre-split-commitment) DB: re-replay required
		}
		live := 0
		for _, b := range tw.bits {
			live += popcount8(b)
		}
		tw.live = live
		// Fast path: a sealed twig with a persistent index trusts its stored
		// roots and skips the per-slot entry scan — O(numTwigs) resume, O(1) RAM.
		if !rebuildIndex && id < activeTwig && haveMeta {
			tw.leafRoot = storedLeafRoot
			tw.bitsRoot = hashBits(&tw.bits)
			tw.root = storedRoot
			tw.dirty = false
			tw.nodes = nil // sealed: no node storage resident
			if hashNode(tw.leafRoot, tw.bitsRoot) != tw.root {
				return errTwigMetaInconsistent
			}
			continue
		}
		// Slow path: materialize the leaf tree. Prefer the persisted blob (it
		// carries dead slots' frozen leaves, whose entry rows may be deleted);
		// fall back to the entry log.
		hydrated := false
		if t.leafStore == nil {
			t.leafStore = LeafStoreFromGetter(g)
		}
		if blob, ok := t.leafStore.Leaves(id); ok && len(blob) == TwigSize*32 {
			for i := 0; i < TwigSize; i++ {
				copy(tw.nodes[TwigSize+i][:], blob[i*32:(i+1)*32])
			}
			hydrated = true
		}
		lo := uint64(id) * TwigSize
		for local := 0; local < TwigSize; local++ {
			slot := lo + uint64(local)
			if slot >= nextSlot {
				break
			}
			needKey := rebuildIndex && tw.bit(uint64(local))
			if hydrated && !needKey {
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
			if !hydrated {
				tw.nodes[TwigSize+local] = hashLeaf(kh, v[32:]) // bulk raw write
			}
			if needKey {
				t.idx.Put(kh, slot)
			}
		}
		tw.recompute() // one full rebuild from the bulk-written leaves
		if haveMeta && tw.root != storedRoot {
			return errTwigMetaInconsistent
		}
		if id < activeTwig {
			tw.nodes = nil // sealed: keep only the roots, free the array
		}
	}
	// Entry records are not retained — start the resident window empty at the
	// append cursor. Future appends grow it; cold serves everything below.
	t.entries = nil
	t.entriesBase = nextSlot
	t.evicted = nextSlot
	t.nextSlot = nextSlot
	t.rootDirty = true
	t.upRebuild = true // twig set replaced wholesale; rebuild the upper heap
	t.Root()           // materialize twig + world roots
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
