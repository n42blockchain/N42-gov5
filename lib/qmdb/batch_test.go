// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import "testing"

// TestBatchFoldEquivalence: batched folding must produce byte-identical roots
// to the eager per-leaf fold across inserts, overwrites and deletes, including
// a Root() taken mid-batch.
func TestBatchFoldEquivalence(t *testing.T) {
	eager := New()
	batched := New()

	rng := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }

	apply := func(tr *Tree, op, k uint64) {
		kh := shKey(k)
		if op%7 == 0 {
			tr.Delete(kh)
		} else {
			tr.Set(kh, shVal(k, op))
		}
	}

	for round := 0; round < 20; round++ {
		batched.BeginLeafBatch()
		for i := 0; i < 1500; i++ {
			op, k := next(), next()%4000
			apply(eager, op, k)
			apply(batched, op, k)
			if round == 7 && i == 750 {
				// Mid-batch Root must settle pending leaves and agree.
				if er, br := eager.Root(), batched.Root(); er != br {
					t.Fatalf("mid-batch root mismatch: eager %x batched %x", er[:8], br[:8])
				}
			}
		}
		batched.EndLeafBatch()
		er, br := eager.Root(), batched.Root()
		if er != br {
			t.Fatalf("round %d: eager root %x != batched %x", round, er[:8], br[:8])
		}
	}
	if eager.LiveCount() != batched.LiveCount() {
		t.Fatalf("live count diverged: %d vs %d", eager.LiveCount(), batched.LiveCount())
	}
	// Proofs from the batched tree verify against the shared root.
	root := batched.Root()
	for k := uint64(0); k < 4000; k += 131 {
		if p, ok := batched.GetProof(shKey(k)); ok {
			if !VerifyProof(root, p) {
				t.Fatalf("batched-tree proof for key %d does not verify", k)
			}
		}
	}
}

// TestFlatIndexVsMap: the flat open-addressing index must behave identically to
// the reference map index under heavy churn (insert/overwrite/delete cycles
// that force growth and tombstone-dominated rehashes).
func TestFlatIndexVsMap(t *testing.T) {
	fi := newFlatIndex()
	mi := newMapIndex()

	rng := uint64(0x243F6A8885A308D3)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }

	for i := 0; i < 200000; i++ {
		k := shKey(next() % 30000)
		switch next() % 4 {
		case 0:
			fi.Delete(k)
			mi.Delete(k)
		default:
			s := next()
			fi.Put(k, s)
			mi.Put(k, s)
		}
	}
	if fi.Len() != mi.Len() {
		t.Fatalf("Len: flat %d != map %d", fi.Len(), mi.Len())
	}
	for i := uint64(0); i < 30000; i++ {
		k := shKey(i)
		fs, fok := fi.Get(k)
		ms, mok := mi.Get(k)
		if fok != mok || fs != ms {
			t.Fatalf("key %d: flat (%d,%v) != map (%d,%v)", i, fs, fok, ms, mok)
		}
	}
	// Absent key with a colliding-prefix probe path.
	var absent Hash
	absent[0] = 0xee
	if _, ok := fi.Get(absent); ok {
		t.Fatal("flat index returned a hit for an absent key")
	}
}
