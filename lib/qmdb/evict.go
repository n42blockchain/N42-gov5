// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Cold-tier eviction for the entry log (P3 memory bounding). QMDB's append-only
// log grows with the number of state MODIFICATIONS, so at chain scale the
// resident entries slice — keyHash+value per slot — is the dominant RAM cost.
// Because entry records are IMMUTABLE once written and a slot's liveness is
// already captured by its twig leaf (nulled on deactivation), the entry records
// for already-FLUSHED slots are not needed in RAM for root computation. They are
// only needed to read a live value (Get/Proof) or to relocate a live entry
// (Compact) — both of which can fault the record back from disk on demand.
//
// EvictThrough drops the entry records below a slot watermark from RAM; entryAt
// then serves those slots from the ColdReader (the persisted positional log).
// Twig leaves stay resident in this tier, so the resident cost falls from
// ~104 B/slot (entry+leaf) to ~32 B/slot (leaf only) plus the live-key index.
// (Twig-leaf eviction and an on-disk index are the remaining tiers.)

package qmdb

import (
	"encoding/binary"
	"sync/atomic"
)

// coldReads counts entry reads served by a ColdReader (one positional-log
// GetOne each); residentHits counts reads served from the resident window.
// Process-wide, observability only (the "qmdb root phases" line). The hit
// counter is striped over padded cache lines: it is bumped on every state
// read by up to 32 Block-STM workers.
var coldReads atomic.Uint64

type paddedCounter struct {
	n atomic.Uint64
	_ [56]byte
}

var residentHits [64]paddedCounter

func noteResidentHit(slot uint64) { residentHits[slot&63].n.Add(1) }

// ReadCounters reports the process-wide totals of cold entry reads and
// resident-window hits.
func ReadCounters() (cold, resident uint64) {
	for i := range residentHits {
		resident += residentHits[i].n.Load()
	}
	return coldReads.Load(), resident
}

// ColdReader serves entry records that have been evicted from the in-memory
// window. ColdEntry returns the immutable (keyHash, value) stored at an absolute
// slot, or ok=false if the slot is absent (e.g. pruned). kv.Tx-backed and
// map-backed implementations both satisfy it; the tree never writes through it
// (persistence is via FlushTo).
type ColdReader interface {
	ColdEntry(slot uint64) (keyHash Hash, value []byte, ok bool)
}

// getterCold adapts a Getter (the persisted positional log) into a ColdReader by
// reading EntryTable[BE8(slot)] = keyHash(32) || value. This is the production
// path: the same kv.Tx that FlushTo writes through serves evicted faults.
type getterCold struct{ g Getter }

func (c getterCold) ColdEntry(slot uint64) (Hash, []byte, bool) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], slot)
	v, err := c.g.GetOne(EntryTable, b[:])
	if err != nil || len(v) < 32 {
		return Hash{}, nil, false
	}
	var kh Hash
	copy(kh[:], v[:32])
	val := make([]byte, len(v)-32)
	copy(val, v[32:])
	return kh, val, true
}

// ColdReaderFromGetter wraps a Getter (kv.Tx / map store) as a ColdReader over the
// flushed entry log, so a tree can evict flushed slots and fault them back.
func ColdReaderFromGetter(g Getter) ColdReader { return getterCold{g} }

// SetCold attaches a cold reader, enabling EvictThrough. With no cold reader the
// window never shifts and the tree behaves exactly as the all-in-RAM original.
func (t *Tree) SetCold(c ColdReader) { t.cold = c }

// EvictThrough drops entry records for slots [entriesBase, through) from RAM,
// reclaiming the backing array. The caller MUST have persisted those slots to
// the cold reader first (e.g. via FlushTo), since entryAt will fault them back
// from cold. No-op without a cold reader or when nothing new can be evicted.
// through is typically the flushed-through cursor.
func (t *Tree) EvictThrough(through uint64) { t.evictThrough(through, false) }

// evictThrough is EvictThrough; amortized (the lagged path) reslices instead
// of copying the retained window into a fresh array unless the dead prefix
// of the backing array outweighs the retained part. With a K-block lag the
// retained window is K blocks of entries, and copying it on every block is
// O(K x block) per block; reslicing makes it O(block) amortized, at the cost
// of keeping at most one window's worth of dropped records alive until the
// next copy or append growth.
func (t *Tree) evictThrough(through uint64, amortized bool) {
	if t.cold == nil || through <= t.entriesBase {
		return
	}
	if through > t.nextSlot {
		through = t.nextSlot
	}
	drop := through - t.entriesBase
	if drop > uint64(len(t.entries)) {
		drop = uint64(len(t.entries))
	}
	if drop == 0 {
		return
	}
	// Copy the retained tail into a fresh, right-sized array so the evicted
	// prefix (and the old backing array) becomes garbage. A plain reslice would
	// keep the whole array alive.
	rem := t.entries[drop:]
	if amortized && uint64(cap(t.entries)-cap(rem)) <= uint64(len(rem)) {
		// Clear the dropped records so their values become garbage now.
		clear(t.entries[:drop])
		t.entries = rem
		t.entriesBase += drop
		t.evicted += drop
		return
	}
	newE := make([]entry, len(rem))
	copy(newE, rem)
	t.entries = newE
	t.entriesBase += drop
	t.evicted += drop
}

// ResidentEntries reports how many entry records are currently held in RAM (the
// window length). Used by tests to assert the footprint stays bounded as history
// grows.
func (t *Tree) ResidentEntries() int { return len(t.entries) }

// AdoptFlushed marks this tree's own appends below `through` as persisted by
// ANOTHER tree that flushed the identical entries at the identical slots (the
// proposer's isolated build tree, after the live tree wrote the block it
// built). The dead-row bookkeeping is dropped -- the flushing tree issued
// those deletes -- and the entries and twig leaves are evicted exactly as
// EvictThrough/EvictTwigsThrough do after a flush of our own.
func (t *Tree) AdoptFlushed(through uint64) {
	t.AdoptFlushedKeep()
	t.EvictThrough(through)
	t.EvictTwigsThrough(through)
}

// AdoptFlushedKeep is AdoptFlushed without the eviction: the dead-row
// bookkeeping of a flush another tree performed is dropped, and the entries
// and twig leaves stay resident until the caller evicts them (the
// N42_QMDB_EVICT_LAG_BLOCKS residency lag).
func (t *Tree) AdoptFlushedKeep() {
	t.deadFlushed = t.deadFlushed[:0]
	t.stagedDead = t.stagedDead[:0]
}

// EntriesBase exposes the absolute slot of the window start (slots below it are
// cold). For tests/diagnostics.
func (t *Tree) EntriesBase() uint64 { return t.entriesBase }

// EvictLag delays entry eviction by K blocks (N42_QMDB_EVICT_LAG_BLOCKS): each
// per-block Evict records the flushed-through cursor of that block and evicts
// entry records only through the cursor recorded K calls earlier,
// clamped to the current flushed cursor (a revert may have lowered it). Entry
// records are immutable and a resident entry's liveness is maintained in place
// by deactivation and revert exactly as for the not-yet-evicted tail, so the
// lag changes only which reads are served from RAM; roots, undo records and
// persisted rows are unaffected. K = 0 evicts everything flushed (EvictThrough
// + EvictTwigsThrough through the current cursor), the original behaviour.
type EvictLag struct {
	K    int
	ring []uint64
}

// Evict records `through` (this block's flushed cursor) and evicts per the
// lag. flushedNow is the tree's current flushed cursor (normally == through).
func (l *EvictLag) Evict(t *Tree, through, flushedNow uint64) {
	if l.K <= 0 {
		t.EvictThrough(through)
		t.EvictTwigsThrough(through)
		return
	}
	// Twig leaves are evicted through the current cursor exactly as without
	// a lag: FlushTo rewrites the meta and leaf blob of EVERY resident twig on
	// every flush, so keeping K blocks of twigs resident would multiply the
	// per-block write volume. Reads need only the entry records.
	t.EvictTwigsThrough(through)
	if flushedNow > t.nextSlot {
		flushedNow = t.nextSlot
	}
	t.flushedResident = flushedNow
	l.ring = append(l.ring, through)
	if len(l.ring) <= l.K {
		return
	}
	w := l.ring[0]
	copy(l.ring, l.ring[1:])
	l.ring = l.ring[:len(l.ring)-1]
	if w > flushedNow {
		w = flushedNow
	}
	t.evictThrough(w, true)
}

// Reset forgets the recorded cursors (the next K evictions are skipped).
func (l *EvictLag) Reset() { l.ring = l.ring[:0] }
