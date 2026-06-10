// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"fmt"
	"testing"
)

func uKey(i int) Hash {
	var k Hash
	k[0] = byte(i)
	k[1] = byte(i >> 8)
	k[31] = 0x5a
	return k
}

func uVal(i, ver int) []byte {
	return []byte(fmt.Sprintf("val-%d-v%d", i, ver))
}

// blockState tracks, per recorded block, what the expected (key → value) map
// and root were, so ProofAt results can be checked against ground truth.
type blockState struct {
	root Hash
	undo *BlockUndo
	live map[Hash][]byte
}

// applyBlocks runs nBlocks of scripted ops with recording, capturing the root
// and live-set snapshot after each block.
func applyBlocks(t *testing.T, tr *Tree, nBlocks int, op func(block int, tr *Tree, live map[Hash][]byte)) []blockState {
	t.Helper()
	live := make(map[Hash][]byte)
	// Snapshot helper.
	snap := func() map[Hash][]byte {
		m := make(map[Hash][]byte, len(live))
		for k, v := range live {
			m[k] = append([]byte(nil), v...)
		}
		return m
	}
	states := make([]blockState, 0, nBlocks+1)
	states = append(states, blockState{root: tr.Root(), live: snap()}) // pre-window base
	for b := 0; b < nBlocks; b++ {
		tr.StartUndoRecording()
		op(b, tr, live)
		undo := tr.StopUndoRecording()
		states = append(states, blockState{root: tr.Root(), undo: undo, live: snap()})
	}
	return states
}

// verifyWindow checks, for every depth into the window, that ProofAt reproduces
// the recorded root byte-exactly and proves the right values for a set of keys.
func verifyWindow(t *testing.T, tr *Tree, states []blockState, keys []Hash) {
	t.Helper()
	head := len(states) - 1
	for target := 0; target < head; target++ {
		undos := make([]*BlockUndo, 0, head-target)
		for b := target + 1; b <= head; b++ {
			undos = append(undos, states[b].undo)
		}
		st := states[target]
		for _, k := range keys {
			proof, root, found, err := tr.ProofAt(k, undos)
			if err != nil {
				t.Fatalf("target %d key %x: ProofAt: %v", target, k[:4], err)
			}
			if root != st.root {
				t.Fatalf("target %d: reconstructed root %x != recorded %x", target, root[:8], st.root[:8])
			}
			wantVal, wantLive := st.live[k]
			if found != wantLive {
				t.Fatalf("target %d key %x: found=%v want %v", target, k[:4], found, wantLive)
			}
			if !found {
				continue
			}
			if !bytes.Equal(proof.Value, wantVal) {
				t.Fatalf("target %d key %x: value %q != want %q", target, k[:4], proof.Value, wantVal)
			}
			if !VerifyProof(root, proof) {
				t.Fatalf("target %d key %x: proof does not verify against target root", target, k[:4])
			}
		}
	}
}

// TestProofAtWindow: overwrites, deletes, and creations across a 12-block
// window; every historical root must be reproduced byte-exactly and every key
// proven with the value it had at that time.
func TestProofAtWindow(t *testing.T) {
	tr := New()
	// Pre-window population: 50 keys.
	for i := 0; i < 50; i++ {
		tr.Set(uKey(i), uVal(i, 0))
	}
	keys := make([]Hash, 0, 60)
	for i := 0; i < 60; i++ {
		keys = append(keys, uKey(i)) // includes 50..59 not yet created
	}

	states := applyBlocks(t, tr, 12, func(b int, tr *Tree, live map[Hash][]byte) {
		// (Maintain ground truth `live` alongside the ops.)
		if b == 0 {
			for i := 0; i < 50; i++ {
				live[uKey(i)] = uVal(i, 0)
			}
		}
		// Overwrite three rotating keys.
		for j := 0; j < 3; j++ {
			i := (b*3 + j) % 50
			tr.Set(uKey(i), uVal(i, b+1))
			live[uKey(i)] = uVal(i, b+1)
		}
		// Create one new key per block.
		ni := 50 + b%10
		tr.Set(uKey(ni), uVal(ni, b+1))
		live[uKey(ni)] = uVal(ni, b+1)
		// Delete one key every other block (recreated by the rotation later).
		if b%2 == 1 {
			di := (b * 7) % 50
			tr.Delete(uKey(di))
			delete(live, uKey(di))
		}
	})
	// Block 0's ground truth setup above ran INSIDE block 0's recording but the
	// pre-window Sets happened before applyBlocks — fix the base snapshot.
	for i := 0; i < 50; i++ {
		states[0].live[uKey(i)] = uVal(i, 0)
	}

	verifyWindow(t, tr, states, keys)
}

// TestProofAtTwigGrowth: the window spans twig-boundary growth (and an upper
// tree capacity doubling), the hardest layout case — targetNextSlot lands in an
// older, smaller forest than the live one.
func TestProofAtTwigGrowth(t *testing.T) {
	tr := New()
	// Fill most of the first twig pre-window.
	for i := 0; i < TwigSize-20; i++ {
		tr.Set(uKey(i), uVal(i, 0))
	}
	var sample []Hash
	for i := 0; i < TwigSize-20; i += 97 {
		sample = append(sample, uKey(i))
	}
	sample = append(sample, uKey(TwigSize+5)) // created in-window

	states := applyBlocks(t, tr, 8, func(b int, tr *Tree, live map[Hash][]byte) {
		if b == 0 {
			for i := 0; i < TwigSize-20; i++ {
				live[uKey(i)] = uVal(i, 0)
			}
		}
		// 10 appends per block → crosses into twig 1 (and upCap 1→2) mid-window.
		for j := 0; j < 10; j++ {
			i := TwigSize + b*10 + j
			tr.Set(uKey(i), uVal(i, 1))
			live[uKey(i)] = uVal(i, 1)
		}
		// One overwrite of an old twig-0 key.
		oi := b * 13
		tr.Set(uKey(oi), uVal(oi, b+2))
		live[uKey(oi)] = uVal(oi, b+2)
	})
	for i := 0; i < TwigSize-20; i++ {
		states[0].live[uKey(i)] = uVal(i, 0)
	}

	verifyWindow(t, tr, states, sample)
}

// TestProofAtRejectsBadInput: ordering violations, future undos, and empty
// windows are refused.
func TestProofAtRejectsBadInput(t *testing.T) {
	tr := New()
	tr.Set(uKey(1), uVal(1, 0))

	if _, _, _, err := tr.ProofAt(uKey(1), nil); err == nil {
		t.Fatal("ProofAt accepted an empty undo window")
	}
	a := &BlockUndo{PrevNextSlot: 5}
	b := &BlockUndo{PrevNextSlot: 3}
	if _, _, _, err := tr.ProofAt(uKey(1), []*BlockUndo{a, b}); err == nil {
		t.Fatal("ProofAt accepted out-of-order undos")
	}
	future := &BlockUndo{PrevNextSlot: 99}
	if _, _, _, err := tr.ProofAt(uKey(1), []*BlockUndo{future}); err == nil {
		t.Fatal("ProofAt accepted an undo ahead of the tree")
	}
}

// TestBlockUndoCodecRoundTrip: Marshal → Unmarshal preserves every field.
func TestBlockUndoCodecRoundTrip(t *testing.T) {
	u := &BlockUndo{
		PrevNextSlot: 123456789,
		Entries: []UndoEntry{
			{Slot: 42, KeyHash: uKey(7), Value: []byte("hello")},
			{Slot: 99999, KeyHash: uKey(8), Value: nil},
			{Slot: 7, KeyHash: uKey(9), Value: bytes.Repeat([]byte{0xab}, 120)},
		},
	}
	got, err := UnmarshalBlockUndo(u.Marshal())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.PrevNextSlot != u.PrevNextSlot || len(got.Entries) != len(u.Entries) {
		t.Fatalf("header mismatch: %+v", got)
	}
	for i := range u.Entries {
		if got.Entries[i].Slot != u.Entries[i].Slot ||
			got.Entries[i].KeyHash != u.Entries[i].KeyHash ||
			!bytes.Equal(got.Entries[i].Value, u.Entries[i].Value) {
			t.Fatalf("entry %d mismatch", i)
		}
	}
	if _, err := UnmarshalBlockUndo([]byte{0x02}); err == nil {
		t.Fatal("accepted bad version")
	}
}

// TestProofAtAfterCompaction: Compact() relocations go through Set, so a
// recorded block containing a compaction still reproduces prior roots.
func TestProofAtAfterCompaction(t *testing.T) {
	tr := New()
	// Fill twig 0 fully, then kill most of it so it becomes sparse.
	for i := 0; i < TwigSize; i++ {
		tr.Set(uKey(i), uVal(i, 0))
	}
	for i := 0; i < TwigSize-100; i++ {
		tr.Delete(uKey(i))
	}
	// Open twig 1 so twig 0 is no longer the active twig (Compact never touches
	// the active twig and exits when only one twig exists).
	tr.Set(uKey(TwigSize+1000), uVal(TwigSize+1000, 0))
	preRoot := tr.Root()

	// One recorded block: compaction relocates the 100 survivors and prunes twig 0.
	tr.StartUndoRecording()
	if pruned := tr.Compact(0.25); pruned == 0 {
		t.Skip("compaction did not trigger")
	}
	undo := tr.StopUndoRecording()
	tr.Root()

	// A survivor key: provable at the pre-compaction state, at its OLD slot.
	k := uKey(TwigSize - 50)
	proof, root, found, err := tr.ProofAt(k, []*BlockUndo{undo})
	if err != nil {
		t.Fatalf("ProofAt: %v", err)
	}
	if root != preRoot {
		t.Fatalf("reconstructed pre-compaction root %x != recorded %x", root[:8], preRoot[:8])
	}
	if !found || !bytes.Equal(proof.Value, uVal(TwigSize-50, 0)) {
		t.Fatalf("survivor not proven correctly: found=%v", found)
	}
	if !VerifyProof(root, proof) {
		t.Fatal("proof does not verify")
	}
}
