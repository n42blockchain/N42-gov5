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

import "encoding/binary"

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
func (t *Tree) EvictThrough(through uint64) {
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

// EntriesBase exposes the absolute slot of the window start (slots below it are
// cold). For tests/diagnostics.
func (t *Tree) EntriesBase() uint64 { return t.entriesBase }
