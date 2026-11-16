// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"errors"
	"sort"
	"testing"
)

func (m *mapHistoryStore) DeleteBlockPos(block uint64) error {
	if _, ok := m.blockPos[block]; ok {
		delete(m.blockPos, block)
		m.posKeys = removeU64(m.posKeys, block)
	}
	return nil
}

func (m *mapHistoryStore) DeleteTopBand(block uint64) error {
	if _, ok := m.topBand[block]; ok {
		delete(m.topBand, block)
		m.bandKeys = removeU64(m.bandKeys, block)
	}
	return nil
}

func (m *mapHistoryStore) DeleteKeyVersion(keyHash Hash, pos uint64) error {
	m.keyVers[keyHash] = removeU64(m.keyVers[keyHash], pos)
	return nil
}

func removeU64(s []uint64, v uint64) []uint64 {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// liveJournalTree is a tree driven like the live node: undo recording per
// block, flush + eviction tiers, dead rows retained, no inline recorder.
type liveJournalTree struct {
	t       *Tree
	disk    mapStore
	hstore  *mapHistoryStore
	flushed uint64
	rootAt  map[uint64]Hash
	valueAt map[uint64]map[uint64][]byte
	cur     map[uint64][]byte
	undos   map[uint64]*BlockUndo
}

func newLiveJournalTree(tb testing.TB) *liveJournalTree {
	tb.Helper()
	tr := New()
	disk := newMapStore()
	tr.SetCold(ColdReaderFromGetter(disk))
	tr.SetLeafStore(LeafStoreFromGetter(disk))
	tr.SetRetainDeadEntryRows(true)
	l := &liveJournalTree{t: tr, disk: disk, hstore: newMapHistoryStore(),
		rootAt: map[uint64]Hash{}, valueAt: map[uint64]map[uint64][]byte{}, cur: map[uint64][]byte{}, undos: map[uint64]*BlockUndo{}}
	// Genesis (block 0): appends only.
	tr.StartUndoRecording()
	for k := uint64(0); k < 300; k++ {
		v := hVal(k, 0)
		tr.Set(hKey(k), v)
		l.cur[k] = v
	}
	l.commit(tb, 0, tr.StopUndoRecording())
	return l
}

// apply executes block b's deterministic ops (variant selects a sibling).
func (l *liveJournalTree) apply(b, variant uint64) *BlockUndo {
	l.t.StartUndoRecording()
	for i := uint64(0); i < 260; i++ {
		k := (b*97 + i*13 + variant*7) % 3500
		if i%9 == 8 {
			l.t.Delete(hKey(k))
			delete(l.cur, k)
			continue
		}
		v := hVal(k, b*10+variant)
		l.t.Set(hKey(k), v)
		l.cur[k] = v
	}
	return l.t.StopUndoRecording()
}

func (l *liveJournalTree) commit(tb testing.TB, b uint64, u *BlockUndo) {
	tb.Helper()
	next, _, err := l.t.FlushTo(l.disk, l.flushed)
	if err != nil {
		tb.Fatalf("flush %d: %v", b, err)
	}
	l.t.CommitFlush()
	l.flushed = next
	l.t.EvictThrough(next)
	l.t.EvictTwigsThrough(next)
	if err := RecordBlockHistory(l.hstore, b, u, l.t.NextSlot(), l.t.TopBandRecord()); err != nil {
		tb.Fatalf("record %d: %v", b, err)
	}
	l.undos[b] = u
	l.rootAt[b] = l.t.Root()
	snap := make(map[uint64][]byte, len(l.cur))
	for k, v := range l.cur {
		snap[k] = v
	}
	l.valueAt[b] = snap
}

// revert unwinds block b (live tree + storage + journal), as a sibling switch does.
func (l *liveJournalTree) revert(tb testing.TB, b uint64) {
	tb.Helper()
	u := l.undos[b]
	ft, err := l.t.ApplyUndoWithStorage(l.disk, u, l.flushed)
	if err != nil {
		tb.Fatalf("apply undo %d: %v", b, err)
	}
	l.flushed = ft
	if err := RevertBlockHistory(l.hstore, b, u); err != nil {
		tb.Fatalf("revert history %d: %v", b, err)
	}
	delete(l.undos, b)
	delete(l.rootAt, b)
	l.cur = map[uint64][]byte{}
	for k, v := range l.valueAt[b-1] {
		l.cur[k] = v
	}
	delete(l.valueAt, b)
}

func (l *liveJournalTree) verifyAll(tb testing.TB, prover *Tree) {
	tb.Helper()
	heights := make([]uint64, 0, len(l.rootAt))
	for h := range l.rootAt {
		heights = append(heights, h)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	for _, h := range heights {
		_, root, found, err := prover.ProofAtHeightFrom(l.hstore, hKey(1<<40), h)
		if err != nil || found {
			tb.Fatalf("h=%d absent key: found=%v err=%v", h, found, err)
		}
		if root != l.rootAt[h] {
			tb.Fatalf("h=%d root mismatch\n want=%x\n got =%x", h, l.rootAt[h], root)
		}
		n := 0
		for k := uint64(0); k < 3500 && n < 12; k += 37 {
			want, live := l.valueAt[h][k]
			proof, r, found, err := prover.ProofAtHeightFrom(l.hstore, hKey(k), h)
			if err != nil {
				tb.Fatalf("h=%d key=%d: %v", h, k, err)
			}
			if found != live {
				tb.Fatalf("h=%d key=%d found=%v want %v", h, k, found, live)
			}
			if r != l.rootAt[h] {
				tb.Fatalf("h=%d key=%d root mismatch", h, k)
			}
			if !live {
				continue
			}
			n++
			if !bytes.Equal(proof.Value, want) {
				tb.Fatalf("h=%d key=%d value %q want %q", h, k, proof.Value, want)
			}
			if !VerifyProof(r, proof) {
				tb.Fatalf("h=%d key=%d proof does not verify", h, k)
			}
			// The wire format is the latest-proof format: round-trip it.
			dec, err := UnmarshalProof(proof.Marshal())
			if err != nil || !VerifyEncodedProofForKey(r, hKey(k), proof.Marshal()) || !bytes.Equal(dec.Value, want) {
				tb.Fatalf("h=%d key=%d encoded proof does not verify: %v", h, k, err)
			}
		}
	}
}

// TestUndoDrivenHistoryMatchesEveryHeight: journaling from undo records (the
// live-node path) reproduces every past root and serves verifying proofs, with
// flush/eviction active and dead rows retained, on the live tree and on an
// independent reload of the persisted layout (the RPC prover's view).
func TestUndoDrivenHistoryMatchesEveryHeight(t *testing.T) {
	l := newLiveJournalTree(t)
	for b := uint64(1); b <= 30; b++ {
		l.commit(t, b, l.apply(b, 0))
	}
	l.verifyAll(t, l.t)

	reloaded := New()
	reloaded.SetCold(ColdReaderFromGetter(l.disk))
	reloaded.SetLeafStore(LeafStoreFromGetter(l.disk))
	if err := reloaded.LoadFrom(l.disk); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Root() != l.rootAt[30] {
		t.Fatal("reloaded root differs")
	}
	l.verifyAll(t, reloaded)
}

// TestUndoDrivenHistoryRevertAndSiblingSwitch: unwinding blocks (newest
// first) and applying a different sibling leaves a journal identical in effect
// to one that only ever saw the winning branch.
func TestUndoDrivenHistoryRevertAndSiblingSwitch(t *testing.T) {
	l := newLiveJournalTree(t)
	for b := uint64(1); b <= 12; b++ {
		l.commit(t, b, l.apply(b, 0))
	}
	// Two-deep switch at 11.
	l.revert(t, 12)
	l.revert(t, 11)
	for b := uint64(11); b <= 16; b++ {
		l.commit(t, b, l.apply(b, 1))
	}
	l.verifyAll(t, l.t)

	// Reference: a fresh tree that only ever applied the winning branch.
	ref := newLiveJournalTree(t)
	for b := uint64(1); b <= 16; b++ {
		v := uint64(0)
		if b >= 11 {
			v = 1
		}
		ref.commit(t, b, ref.apply(b, v))
	}
	for b := uint64(0); b <= 16; b++ {
		if ref.rootAt[b] != l.rootAt[b] {
			t.Fatalf("block %d root differs from the winner-only reference", b)
		}
	}
	for tw, blob := range ref.hstore.stamps {
		got := l.hstore.stamps[tw]
		if len(got) != len(blob) {
			got = append(got, make([]byte, len(blob)-len(got))...)
		}
		if !bytes.Equal(got, blob) {
			t.Fatalf("twig %d death stamps differ from the reference", tw)
		}
	}
	for k, vs := range ref.hstore.keyVers {
		if got := l.hstore.keyVers[k]; len(got) != len(vs) {
			t.Fatalf("key versions differ: %v vs %v", got, vs)
		}
	}
}

func TestRecordBlockHistoryRejectsIncompleteUndo(t *testing.T) {
	store := newMapHistoryStore()
	band := make([]byte, 8)
	u := &BlockUndo{PrevNextSlot: 10, AppendedKeys: []Hash{hKey(1)}}
	if err := RecordBlockHistory(store, 3, u, 12, band); !errors.Is(err, ErrHistoryUndoIncomplete) {
		t.Fatalf("cursor mismatch: err = %v", err)
	}
	if err := RecordBlockHistory(store, 3, nil, 12, band); !errors.Is(err, ErrHistoryUndoIncomplete) {
		t.Fatalf("nil undo: err = %v", err)
	}
	if err := RecordBlockHistory(store, 0, &BlockUndo{PrevNextSlot: 5, Entries: []UndoEntry{{Slot: 1}}}, 5, band); err == nil {
		t.Fatal("block 0 deactivation must be refused")
	}
	// Double death of one slot.
	if err := RecordBlockHistory(store, 4, &BlockUndo{PrevNextSlot: 5, Entries: []UndoEntry{{Slot: 1}}}, 5, band); err != nil {
		t.Fatal(err)
	}
	if err := RecordBlockHistory(store, 6, &BlockUndo{PrevNextSlot: 5, Entries: []UndoEntry{{Slot: 1}}}, 5, band); !errors.Is(err, ErrHistoryInconsistent) {
		t.Fatalf("double death: err = %v", err)
	}
	if err := RevertBlockHistory(store, 6, &BlockUndo{PrevNextSlot: 5, Entries: []UndoEntry{{Slot: 1}}}); !errors.Is(err, ErrHistoryInconsistent) {
		t.Fatalf("revert of a foreign death: err = %v", err)
	}
}

// Without the retention switch a leaf-store tree reclaims dead rows (today's
// behaviour); with it, they stay.
func TestRetainDeadEntryRows(t *testing.T) {
	for _, retain := range []bool{false, true} {
		tr := New()
		disk := newMapStore()
		tr.SetCold(ColdReaderFromGetter(disk))
		tr.SetLeafStore(LeafStoreFromGetter(disk))
		tr.SetRetainDeadEntryRows(retain)
		tr.Set(hKey(1), []byte("a"))
		next, _, err := tr.FlushTo(disk, 0)
		if err != nil {
			t.Fatal(err)
		}
		tr.CommitFlush()
		tr.EvictThrough(next)
		tr.Set(hKey(1), []byte("b")) // kills slot 0
		if _, _, err := tr.FlushTo(disk, next); err != nil {
			t.Fatal(err)
		}
		tr.CommitFlush()
		row, _ := disk.GetOne(EntryTable, be8(0))
		if (row != nil) != retain {
			t.Fatalf("retain=%v: dead row present=%v", retain, row != nil)
		}
	}
}
