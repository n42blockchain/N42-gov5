// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

// withApplyWorkers runs fn with ParallelApplyWorkers = w.
func withApplyWorkers(w int, fn func()) {
	prev := ParallelApplyWorkers
	ParallelApplyWorkers = w
	defer func() { ParallelApplyWorkers = prev }()
	fn()
}

// paBlock builds one sorted, key-distinct batch of n ops over a key space of
// `space`: inserts, updates, deletes, and re-inserts of deleted keys, with
// value lengths that hit every leaf-hash path (one block, two blocks, hasher).
func paBlock(rng *rand.Rand, n int, space uint64, deleted map[uint64]bool) []Op {
	seen := make(map[uint64]bool, n)
	ops := make([]Op, 0, n)
	for len(ops) < n {
		k := rng.Uint64() % space
		if seen[k] {
			continue
		}
		seen[k] = true
		op := Op{KeyHash: shKey(k)}
		if rng.Intn(6) == 0 && !deleted[k] {
			deleted[k] = true // delete (no-op if never inserted)
		} else {
			delete(deleted, k) // insert / update / re-insert
			lens := [...]int{1, 31, 32, 70, 95, 96, 200}
			v := make([]byte, lens[rng.Intn(len(lens))])
			rng.Read(v)
			v[0] |= 1 // non-empty value
			op.Value = v
		}
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
	return ops
}

type paNode struct {
	tr      *Tree
	store   mapStore
	flushed uint64
	undos   []*BlockUndo
	roots   []Hash
}

func newPANode(mapIdx bool) *paNode {
	n := &paNode{tr: New(), store: newMapStore()}
	if mapIdx {
		n.tr.SetIndex(newMapIndex())
	}
	n.tr.SetCold(ColdReaderFromGetter(n.store))
	n.tr.SetLeafStore(LeafStoreFromGetter(n.store))
	return n
}

func (n *paNode) apply(t *testing.T, ops []Op, workers int, evict bool) {
	t.Helper()
	n.roots = append(n.roots, n.tr.Root())
	n.tr.StartUndoRecording()
	withApplyWorkers(workers, func() { n.tr.ApplyOps(ops) })
	n.undos = append(n.undos, n.tr.StopUndoRecording())
	var err error
	if n.flushed, _, err = n.tr.FlushTo(n.store, n.flushed); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if evict {
		n.tr.EvictThrough(n.flushed)
		n.tr.EvictTwigsThrough(n.flushed)
	}
}

// TestParallelApplyEquivalence: for every batch size and worker count, the
// parallel apply reproduces the sequential root, undo records and persisted
// rows byte-for-byte, and a storage-backed revert + re-apply reproduces the
// root again.
func TestParallelApplyEquivalence(t *testing.T) {
	sizes := []int{1, 10, 1000, 50000}
	if testing.Short() {
		sizes = sizes[:3]
	}
	for _, size := range sizes {
		for _, workers := range []int{1, 2, 7, 16} {
			for _, mapIdx := range []bool{false, true} {
				name := fmt.Sprintf("size%d/w%d/mapidx=%v", size, workers, mapIdx)
				t.Run(name, func(t *testing.T) {
					paEquivalence(t, size, workers, mapIdx)
				})
			}
		}
	}
}

func paEquivalence(t *testing.T, size, workers int, mapIdx bool) {
	rng := rand.New(rand.NewSource(int64(size*131 + workers)))
	space := uint64(size)*3 + 8
	deleted := map[uint64]bool{}
	seq, par := newPANode(mapIdx), newPANode(mapIdx)
	blocks := 6
	if size >= 50000 {
		blocks = 4
	}
	var all [][]Op
	for b := 0; b < blocks; b++ {
		ops := paBlock(rng, size, space, deleted)
		all = append(all, ops)
		evict := b%2 == 1 // alternate resident and cold old slots
		seq.apply(t, ops, 0, evict)
		par.apply(t, ops, workers, evict)
		if s, p := seq.tr.Root(), par.tr.Root(); s != p {
			t.Fatalf("block %d: root seq %x != par %x", b, s[:8], p[:8])
		}
		if s, p := seq.undos[b].Marshal(), par.undos[b].Marshal(); !bytes.Equal(s, p) {
			t.Fatalf("block %d: undo records differ (%d vs %d bytes)", b, len(s), len(p))
		}
		if seq.tr.NextSlot() != par.tr.NextSlot() || seq.tr.LiveCount() != par.tr.LiveCount() {
			t.Fatalf("block %d: cursor/live diverged", b)
		}
		if !reflect.DeepEqual(seq.store, par.store) {
			t.Fatalf("block %d: persisted rows differ", b)
		}
	}
	// Revert the last two blocks (storage-backed), then re-apply them.
	for _, n := range []*paNode{seq, par} {
		for b := blocks - 1; b >= blocks-2; b-- {
			var err error
			if n.flushed, err = n.tr.ApplyUndoWithStorage(n.store, n.undos[b], n.flushed); err != nil {
				t.Fatalf("revert block %d: %v", b, err)
			}
			if got := n.tr.Root(); got != n.roots[b] {
				t.Fatalf("revert block %d: root %x != pre-block %x", b, got[:8], n.roots[b][:8])
			}
		}
	}
	if !reflect.DeepEqual(seq.store, par.store) {
		t.Fatalf("persisted rows differ after revert")
	}
	for b := blocks - 2; b < blocks; b++ {
		seq.apply(t, all[b], 0, true)
		par.apply(t, all[b], workers, true)
		if s, p := seq.tr.Root(), par.tr.Root(); s != p {
			t.Fatalf("re-apply block %d: root seq %x != par %x", b, s[:8], p[:8])
		}
		if !bytes.Equal(seq.undos[len(seq.undos)-1].Marshal(), par.undos[len(par.undos)-1].Marshal()) {
			t.Fatalf("re-apply block %d: undo records differ", b)
		}
	}
	if !reflect.DeepEqual(seq.store, par.store) {
		t.Fatalf("persisted rows differ after re-apply")
	}
	// A fresh reload of the parallel node's store agrees with its live root.
	fresh := New()
	fresh.SetCold(ColdReaderFromGetter(par.store))
	if err := fresh.LoadFrom(par.store); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, want := fresh.Root(), par.tr.Root(); got != want {
		t.Fatalf("reload root %x != live %x", got[:8], want[:8])
	}
}

// TestParallelApplyFallback: unsorted or duplicate-key batches take the
// sequential path and still match it.
func TestParallelApplyFallback(t *testing.T) {
	ops := []Op{{KeyHash: shKey(2), Value: []byte{1}}, {KeyHash: shKey(1), Value: []byte{2}}, {KeyHash: shKey(2), Value: []byte{3}}}
	a, b := New(), New()
	a.ApplyOps(ops)
	withApplyWorkers(8, func() {
		if b.parallelApplyEligible(ops) {
			t.Fatal("unsorted/duplicate batch reported eligible")
		}
		b.ApplyOps(ops)
	})
	if a.Root() != b.Root() {
		t.Fatal("fallback root mismatch")
	}
}

// BenchmarkParallelApply measures ApplyOps on a 2M-key tree with undo
// recording and an evicted-to-cold history, at 16k-op blocks (the fleet's
// median follower block).
func BenchmarkParallelApply(b *testing.B) {
	for _, w := range []int{0, 1, 4, 8, 16} {
		b.Run(fmt.Sprintf("w%d", w), func(b *testing.B) {
			rng := rand.New(rand.NewSource(1))
			const space = 2_000_000
			n := newPANode(false)
			deleted := map[uint64]bool{}
			// Base: insert the key space in big sorted batches.
			for lo := uint64(0); lo < space; lo += 200_000 {
				ops := make([]Op, 0, 200_000)
				for k := lo; k < lo+200_000; k++ {
					ops = append(ops, Op{KeyHash: shKey(k), Value: shVal(k, 0)})
				}
				sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
				n.tr.ApplyOps(ops)
			}
			blocks := make([][]Op, 8)
			for i := range blocks {
				blocks[i] = paBlock(rng, 16000, space, deleted)
			}
			var err error
			if n.flushed, _, err = n.tr.FlushTo(n.store, 0); err != nil {
				b.Fatal(err)
			}
			n.tr.EvictThrough(n.flushed)
			n.tr.EvictTwigsThrough(n.flushed)
			var total time.Duration
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ops := blocks[i%len(blocks)]
				n.tr.StartUndoRecording()
				t0 := time.Now()
				withApplyWorkers(w, func() { n.tr.ApplyOps(ops) })
				total += time.Since(t0)
				n.tr.StopUndoRecording()
				b.StopTimer()
				if n.flushed, _, err = n.tr.FlushTo(n.store, n.flushed); err != nil {
					b.Fatal(err)
				}
				n.tr.EvictThrough(n.flushed)
				n.tr.EvictTwigsThrough(n.flushed)
				b.StartTimer()
			}
			b.ReportMetric(float64(total.Microseconds())/float64(b.N)/1000, "apply-ms/block")
		})
	}
}
