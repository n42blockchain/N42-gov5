// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// withShardApplyWorkers runs fn with ShardApplyWorkers = w.
func withShardApplyWorkers(w int, fn func()) {
	prev := ShardApplyWorkers
	ShardApplyWorkers = w
	defer func() { ShardApplyWorkers = prev }()
	fn()
}

// saNode is one tree of the sharded-apply differential test.
type saNode struct {
	tr      *Tree
	store   mapStore
	hist    *mapHistoryStore
	flushed uint64
	undos   []*BlockUndo
	roots   []Hash
	block   uint64
}

func newSANode(idx string, hist bool) *saNode {
	n := &saNode{tr: New(), store: newMapStore()}
	switch idx {
	case "map":
		n.tr.SetIndex(newMapIndex())
	case "sharded":
		n.tr.SetIndex(newShardedMapIndex(0))
	}
	n.tr.SetCold(ColdReaderFromGetter(n.store))
	n.tr.SetLeafStore(LeafStoreFromGetter(n.store))
	if hist {
		n.hist = newMapHistoryStore()
		n.tr.SetHistoryRecorder(NewHistoryRecorder(n.hist))
	}
	return n
}

func (n *saNode) apply(t *testing.T, ops []Op, workers int, evict bool) {
	t.Helper()
	n.block++
	if err := n.tr.BeginBlock(n.block); err != nil {
		t.Fatal(err)
	}
	n.roots = append(n.roots, n.tr.Root())
	n.tr.StartUndoRecording()
	withShardApplyWorkers(workers, func() { n.tr.ApplyOps(ops) })
	n.undos = append(n.undos, n.tr.StopUndoRecording())
	if err := n.tr.EndBlock(); err != nil {
		t.Fatal(err)
	}
	var err error
	if n.flushed, _, err = n.tr.FlushTo(n.store, n.flushed); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if evict {
		n.tr.EvictThrough(n.flushed)
		n.tr.EvictTwigsThrough(n.flushed)
	}
}

// saBlock builds one sorted, key-distinct batch of n ops over `space` keys:
// inserts, updates of keys written in earlier blocks, deletes and
// re-inserts, with value lengths on every leaf-hash path.
func saBlock(rng *rand.Rand, n int, space uint64, deleted map[uint64]bool) []Op {
	seen := make(map[uint64]bool, n)
	ops := make([]Op, 0, n)
	lens := [...]int{1, 20, 31, 32, 33, 70, 95, 96, 200}
	for len(ops) < n {
		k := rng.Uint64() % space
		if seen[k] {
			continue
		}
		seen[k] = true
		op := Op{KeyHash: abKey(k)}
		if rng.Intn(7) == 0 && !deleted[k] {
			deleted[k] = true // delete (a no-op when never inserted)
		} else {
			delete(deleted, k)
			v := make([]byte, lens[rng.Intn(len(lens))])
			rng.Read(v)
			v[0] |= 1
			op.Value = v
		}
		ops = append(ops, op)
	}
	sortOps(ops)
	return ops
}

// saSameIndex asserts both trees map every key of the space identically.
func saSameIndex(t *testing.T, a, b *Tree, space uint64) {
	t.Helper()
	if a.idx.Len() != b.idx.Len() {
		t.Fatalf("index len %d != %d", a.idx.Len(), b.idx.Len())
	}
	for k := uint64(0); k < space; k++ {
		kh := abKey(k)
		sa, oka := a.idx.Get(kh)
		sb, okb := b.idx.Get(kh)
		if oka != okb || sa != sb {
			t.Fatalf("index key %d: (%d,%v) != (%d,%v)", k, sa, oka, sb, okb)
		}
	}
}

func saCompare(t *testing.T, what string, seq, sh *saNode, space uint64) {
	t.Helper()
	if s, p := seq.tr.Root(), sh.tr.Root(); s != p {
		t.Fatalf("%s: root seq %x != sharded %x", what, s[:8], p[:8])
	}
	if seq.tr.NextSlot() != sh.tr.NextSlot() || seq.tr.LiveCount() != sh.tr.LiveCount() || seq.tr.LiveBits() != sh.tr.LiveBits() {
		t.Fatalf("%s: cursor/live diverged", what)
	}
	if !reflect.DeepEqual(seq.store, sh.store) {
		t.Fatalf("%s: persisted rows differ", what)
	}
	if !reflect.DeepEqual(seq.hist, sh.hist) {
		t.Fatalf("%s: history journal differs", what)
	}
	if !reflect.DeepEqual(seq.tr.deadFlushed, sh.tr.deadFlushed) {
		t.Fatalf("%s: deadFlushed differs", what)
	}
	saSameIndex(t, seq.tr, sh.tr, space)
}

// TestShardApplyEquivalence: over many random blocks, the sharded apply
// reproduces the sequential path's root, undo record bytes, persisted rows,
// index contents and history journal, for every in-RAM index shape and
// worker count; a storage-backed revert of the last blocks and a re-apply of
// different sibling blocks agree as well.
func TestShardApplyEquivalence(t *testing.T) {
	sizes := []int{100, 1500, 6000}
	workers := []int{1, 2, 3, 8, 32}
	blocks := 8
	if testing.Short() {
		sizes, workers, blocks = []int{1500, 6000}, []int{2, 8}, 5
	}
	for _, idx := range []string{"sharded", "map", "flat"} {
		for _, hist := range []bool{false, true} {
			for _, size := range sizes {
				for _, w := range workers {
					t.Run(fmt.Sprintf("%s/hist=%v/n%d/w%d", idx, hist, size, w), func(t *testing.T) {
						saEquivalence(t, idx, hist, size, w, blocks)
					})
				}
			}
		}
	}
}

func saEquivalence(t *testing.T, idx string, hist bool, size, workers, blocks int) {
	rng := rand.New(rand.NewSource(int64(size*977 + workers*31 + len(idx))))
	space := uint64(size) * 2
	deleted := map[uint64]bool{}
	seq, sh := newSANode(idx, hist), newSANode(idx, hist)
	for b := 0; b < blocks; b++ {
		ops := saBlock(rng, size, space, deleted)
		evict := b%3 == 2 // mix resident and cold old slots
		seq.apply(t, ops, 0, evict)
		sh.apply(t, ops, workers, evict)
		if s, p := seq.undos[b].Marshal(), sh.undos[b].Marshal(); !bytes.Equal(s, p) {
			t.Fatalf("block %d: undo records differ (%d vs %d bytes)", b, len(s), len(p))
		}
		if size >= 2*shardApplyMinChunk && sh.tr.LastApplyPhases().Workers == 0 {
			t.Fatalf("block %d: sharded path not taken", b)
		}
		saCompare(t, fmt.Sprintf("block %d", b), seq, sh, space)
	}
	if hist {
		return // history trees are append-only archives; no revert
	}
	// Revert the last two blocks through storage, then apply different
	// sibling blocks on top.
	for _, n := range []*saNode{seq, sh} {
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
	saCompare(t, "after revert", seq, sh, space)
	sib := map[uint64]bool{}
	for b := 0; b < 3; b++ {
		ops := saBlock(rng, size, space, sib)
		seq.apply(t, ops, 0, b == 1)
		sh.apply(t, ops, workers, b == 1)
		if !bytes.Equal(seq.undos[len(seq.undos)-1].Marshal(), sh.undos[len(sh.undos)-1].Marshal()) {
			t.Fatalf("sibling block %d: undo records differ", b)
		}
		saCompare(t, fmt.Sprintf("sibling block %d", b), seq, sh, space)
	}
	// A fresh reload of the sharded node's store agrees with its live root.
	fresh := New()
	fresh.SetCold(ColdReaderFromGetter(sh.store))
	if err := fresh.LoadFrom(sh.store); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, want := fresh.Root(), sh.tr.Root(); got != want {
		t.Fatalf("reload root %x != live %x", got[:8], want[:8])
	}
}

// TestShardApplyFallback: batches the sharded path must not take (unsorted
// or duplicate keys, too few ops, an evicted append twig) run the existing
// path and still match it.
func TestShardApplyFallback(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	ops := saBlock(rng, 3000, 9000, map[uint64]bool{})
	ops[10], ops[11] = ops[11], ops[10]
	dup := append([]Op(nil), ops[:2000]...)
	dup[5].KeyHash = dup[4].KeyHash
	for name, batch := range map[string][]Op{"unsorted": ops, "duplicate": dup, "small": ops[:100]} {
		a, b := New(), New()
		a.ApplyOps(batch)
		withShardApplyWorkers(8, func() {
			if b.shardApplyEligible(batch) {
				t.Fatalf("%s batch reported eligible", name)
			}
			b.ApplyOps(batch)
		})
		if a.Root() != b.Root() {
			t.Fatalf("%s: fallback root mismatch", name)
		}
	}
	// A dirty append twig (ForceDirty): the sequential path's business.
	prev := saBlock(rng, 3000, 9000, map[uint64]bool{})
	next := saBlock(rng, 3000, 9000, map[uint64]bool{})
	a, b := New(), New()
	a.ApplyOps(prev)
	withShardApplyWorkers(8, func() { b.ApplyOps(prev) })
	a.ForceDirty()
	b.ForceDirty()
	a.ApplyOps(next)
	withShardApplyWorkers(8, func() {
		if b.shardApplyEligible(next) {
			t.Fatal("batch over a dirty append twig reported eligible")
		}
		b.ApplyOps(next)
	})
	if a.Root() != b.Root() {
		t.Fatal("dirty-twig fallback root mismatch")
	}
}

// TestShardChunksOnShardBoundaries: with the sharded index no index shard
// spans two chunks, and the chunks tile the op list.
func TestShardChunksOnShardBoundaries(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	ops := saBlock(rng, 40000, 1<<40, map[uint64]bool{})
	for _, w := range []int{1, 2, 7, 32, 256} {
		cs := shardChunks(ops, w, true)
		if cs[0].lo != 0 || cs[len(cs)-1].hi != len(ops) {
			t.Fatalf("w%d: chunks do not cover the ops", w)
		}
		for i := 1; i < len(cs); i++ {
			if cs[i].lo != cs[i-1].hi || cs[i].lo >= cs[i].hi {
				t.Fatalf("w%d: chunk %d not contiguous", w, i)
			}
			if ops[cs[i].lo].KeyHash[0] == ops[cs[i].lo-1].KeyHash[0] {
				t.Fatalf("w%d: chunk %d splits shard %d", w, i, ops[cs[i].lo].KeyHash[0])
			}
		}
	}
}
