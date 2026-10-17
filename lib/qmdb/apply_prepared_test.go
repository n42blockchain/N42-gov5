// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// TestPreparedApplyEquivalence: PrepareOps + CommitPrepared + FinishBatch,
// with the leaf hashes computed in the prepare or left to the commit/fold,
// reproduces sequential ApplyOps' roots, undo records and persisted rows.
func TestPreparedApplyEquivalence(t *testing.T) {
	for _, size := range []int{1, 1000, 20000} {
		for _, workers := range []int{1, 4} {
			for _, hashLeaves := range []bool{false, true} {
				for _, mapIdx := range []bool{false, true} {
					t.Run(fmt.Sprintf("size%d/w%d/leaves=%v/mapidx=%v", size, workers, hashLeaves, mapIdx), func(t *testing.T) {
						rng := rand.New(rand.NewSource(int64(size + workers)))
						space := uint64(size)*3 + 8
						deleted := map[uint64]bool{}
						seq, prep := newPANode(mapIdx), newPANode(mapIdx)
						for b := 0; b < 5; b++ {
							ops := paBlock(rng, size, space, deleted)
							seq.apply(t, ops, 0, b%2 == 1)

							prep.roots = append(prep.roots, prep.tr.Root())
							p := prep.tr.PrepareOps(ops, true, workers, hashLeaves)
							if p == nil {
								t.Fatal("sorted distinct batch not preparable")
							}
							prep.tr.StartUndoRecording()
							if err := prep.tr.CommitPrepared(p); err != nil {
								t.Fatal(err)
							}
							prep.undos = append(prep.undos, prep.tr.StopUndoRecording())
							root := prep.tr.FinishBatch()
							var err error
							if prep.flushed, _, err = prep.tr.FlushTo(prep.store, prep.flushed); err != nil {
								t.Fatal(err)
							}
							if b%2 == 1 {
								prep.tr.EvictThrough(prep.flushed)
								prep.tr.EvictTwigsThrough(prep.flushed)
							}
							if want := seq.tr.Root(); root != want {
								t.Fatalf("block %d: root %x, sequential %x", b, root, want)
							}
							if !reflect.DeepEqual(seq.undos[b], prep.undos[b]) {
								t.Fatalf("block %d: undo records differ", b)
							}
						}
						if !reflect.DeepEqual(seq.store, prep.store) {
							t.Fatal("persisted rows differ")
						}
						for k := uint64(0); k < space; k++ {
							a, aok := seq.tr.Get(shKey(k))
							b, bok := prep.tr.Get(shKey(k))
							if aok != bok || string(a) != string(b) {
								t.Fatalf("key %d: values differ", k)
							}
						}
					})
				}
			}
		}
	}
}

// TestPreparedStale: a batch prepared before a mutation is refused untouched.
func TestPreparedStale(t *testing.T) {
	n := newPANode(false)
	rng := rand.New(rand.NewSource(7))
	ops := paBlock(rng, 100, 1000, map[uint64]bool{})
	p := n.tr.PrepareOps(ops, false, 1, false)
	n.tr.Set(shKey(5000), []byte{1})
	before := n.tr.Root()
	if err := n.tr.CommitPrepared(p); !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("err = %v, want ErrPreparedStale", err)
	}
	if n.tr.Root() != before {
		t.Fatal("stale commit changed the tree")
	}
	unsorted := []Op{{KeyHash: shKey(2), Value: []byte{1}}, {KeyHash: shKey(1), Value: []byte{1}}}
	if n.tr.CanPrepare(unsorted) && n.tr.PrepareOps(unsorted, false, 1, false) != nil {
		t.Fatal("unsorted batch prepared")
	}
}
