// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"math/rand"
	"runtime"
	"sort"
	"testing"
	"time"
)

// lagRig is one tree + store + residency lag driven block by block exactly as
// the production write path does: record undo, ApplyOps, FlushTo, evict.
type lagRig struct {
	tr      *Tree
	store   mapStore
	flushed uint64
	lag     EvictLag
	undos   []*BlockUndo
	roots   []Hash
}

func newLagRig(k int) *lagRig {
	st := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(st))
	tr.SetLeafStore(LeafStoreFromGetter(st))
	return &lagRig{tr: tr, store: st, lag: EvictLag{K: k}}
}

func (r *lagRig) block(t *testing.T, ops []Op) {
	t.Helper()
	r.tr.StartUndoRecording()
	r.tr.ApplyOps(ops)
	u := r.tr.StopUndoRecording()
	next, _, err := r.tr.FlushTo(r.store, r.flushed)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	r.flushed = next
	r.lag.Evict(r.tr, next, r.flushed)
	r.undos = append(r.undos, u)
	r.roots = append(r.roots, r.tr.Root())
}

func (r *lagRig) revert(t *testing.T) {
	t.Helper()
	n := len(r.undos) - 1
	f, err := r.tr.ApplyUndoWithStorage(r.store, r.undos[n], r.flushed)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	r.flushed = f
	r.undos = r.undos[:n]
	r.roots = r.roots[:n]
}

func storesEqual(a, b mapStore) bool {
	if len(a) != len(b) {
		return false
	}
	for tb, ma := range a {
		mb := b[tb]
		if len(ma) != len(mb) {
			return false
		}
		for k, v := range ma {
			if w, ok := mb[k]; !ok || !bytes.Equal(v, w) {
				return false
			}
		}
	}
	return true
}

func lagKey(i uint64) Hash {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], i)
	return Hash(sha256.Sum256(b[:]))
}

// lagOps builds one block: `bounded` rewrites/deletes from a fixed key set (the
// fleet's senders+recipients), `fresh` inserts of never-seen keys.
func lagOps(rng *rand.Rand, b uint64, bounded, fresh int) []Op {
	seen := map[Hash]bool{}
	var ops []Op
	for i := 0; i < bounded; i++ {
		kh := lagKey(uint64(rng.Intn(1500)))
		if seen[kh] {
			continue
		}
		seen[kh] = true
		if rng.Intn(10) == 0 {
			ops = append(ops, Op{KeyHash: kh})
		} else {
			ops = append(ops, Op{KeyHash: kh, Value: []byte{byte(b), byte(b >> 8), byte(i), 7}})
		}
	}
	for i := 0; i < fresh; i++ {
		ops = append(ops, Op{KeyHash: lagKey(1<<32 + b*uint64(fresh) + uint64(i)), Value: []byte{byte(i), 1}})
	}
	sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
	return ops
}

func genBlocks(seed int64, n, bounded, fresh int) [][]Op {
	rng := rand.New(rand.NewSource(seed))
	out := make([][]Op, n)
	for b := range out {
		out[b] = lagOps(rng, uint64(b), bounded, fresh)
	}
	return out
}

func assertSame(t *testing.T, what string, ref, got *lagRig) {
	t.Helper()
	if ref.tr.Root() != got.tr.Root() {
		t.Fatalf("%s: lag %d root %x != lag 0 %x", what, got.lag.K, got.tr.Root(), ref.tr.Root())
	}
	if ref.flushed != got.flushed || ref.tr.NextSlot() != got.tr.NextSlot() {
		t.Fatalf("%s: lag %d cursors differ", what, got.lag.K)
	}
	if !storesEqual(ref.store, got.store) {
		for tb, m := range ref.store {
			for k, v := range m {
				if !bytes.Equal(got.store[tb][k], v) {
					t.Logf("table %s key %x: ref %x got %x", tb, k, v, got.store[tb][k])
				}
			}
		}
		for tb, m := range got.store {
			for k := range m {
				if _, ok := ref.store[tb][k]; !ok {
					t.Logf("table %s key %x only in lag", tb, k)
				}
			}
		}
		t.Fatalf("%s: lag %d persisted store differs (block %d)", what, got.lag.K, len(got.roots))
	}
}

// TestEvictLagEquivalence: lags 1, 8, 64 against lag 0 on bounded and growing
// workloads -- identical roots, undo bytes and persisted store after every
// block; a 256-block (undo window) newest-first unwind identical at every
// step; revert-then-reapply identical; reopen identical.
func TestEvictLagEquivalence(t *testing.T) {
	t.Run("serial", testEvictLagEquivalence)
	t.Run("parallelApply", func(t *testing.T) {
		withApplyWorkers(4, func() { testEvictLagEquivalence(t) })
	})
}

func testEvictLagEquivalence(t *testing.T) {
	const W = 256
	n := 300
	if testing.Short() {
		n = 120
	}
	for _, wl := range []struct {
		name           string
		bounded, fresh int
	}{{"bounded", 200, 0}, {"growing", 100, 60}} {
		for seed := int64(1); seed <= 2; seed++ {
			blocks := genBlocks(seed, n, wl.bounded, wl.fresh)
			extra := genBlocks(seed+100, 80, wl.bounded, wl.fresh)
			for _, k := range []int{1, 8, 64} {
				ref, got := newLagRig(0), newLagRig(k)
				for b, ops := range blocks {
					ref.block(t, ops)
					got.block(t, ops)
					if !bytes.Equal(ref.undos[b].Marshal(), got.undos[b].Marshal()) {
						t.Fatalf("%s/%d lag %d block %d: undo bytes differ", wl.name, seed, k, b)
					}
					assertSame(t, wl.name, ref, got)
				}
				if got.tr.ResidentEntries() <= ref.tr.ResidentEntries() {
					t.Fatalf("lag %d kept no more resident than lag 0 (%d vs %d)", k, got.tr.ResidentEntries(), ref.tr.ResidentEntries())
				}
				// Reopen.
				re := New()
				re.SetCold(ColdReaderFromGetter(got.store))
				re.SetLeafStore(LeafStoreFromGetter(got.store))
				if err := re.LoadFrom(got.store); err != nil || re.Root() != ref.tr.Root() {
					t.Fatalf("reopen lag %d: err=%v root mismatch", k, err)
				}
				// Revert d blocks, check against the recorded root, reapply
				// fresh blocks through the (now stale) lag ring.
				for _, d := range []int{1, 7, k, k + 1, 100} {
					if d > len(ref.undos) {
						continue
					}
					for i := 0; i < d; i++ {
						ref.revert(t)
						got.revert(t)
						assertSame(t, "revert", ref, got)
					}
					if want := ref.roots[len(ref.roots)-1]; got.tr.Root() != want {
						t.Fatalf("revert %d: root != recorded", d)
					}
					for _, ops := range extra[:d%len(extra)+1] {
						ref.block(t, ops)
						got.block(t, ops)
						assertSame(t, "reapply", ref, got)
					}
				}
				// Full undo-window unwind, step by step.
				for i := 0; i < W && len(ref.undos) > 0; i++ {
					ref.revert(t)
					got.revert(t)
					assertSame(t, "unwind", ref, got)
				}
			}
		}
	}
}

// TestEvictLagResidency: with lag K, keys last written within the last K
// blocks are served from RAM; keys older than the lag are cold reads.
func TestEvictLagResidency(t *testing.T) {
	const K = 8
	r := newLagRig(K)
	for b := uint64(0); b < 40; b++ {
		ops := make([]Op, 0, 50)
		for i := uint64(0); i < 50; i++ {
			ops = append(ops, Op{KeyHash: lagKey(b*1000 + i), Value: []byte{byte(b), byte(i)}})
		}
		sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
		r.block(t, ops)
	}
	read := func(b uint64) (cold, hits uint64) {
		c0, h0 := ReadCounters()
		for i := uint64(0); i < 50; i++ {
			if _, ok, _ := r.tr.GetVia(lagKey(b*1000+i), nil); !ok {
				t.Fatalf("key of block %d missing", b)
			}
		}
		c1, h1 := ReadCounters()
		return c1 - c0, h1 - h0
	}
	for b := uint64(40 - K); b < 40; b++ {
		if c, h := read(b); c != 0 || h != 50 {
			t.Fatalf("block %d (within lag): cold=%d hits=%d, want 0/50", b, c, h)
		}
	}
	for b := uint64(0); b < 40-K; b++ {
		if c, h := read(b); c != 50 || h != 0 {
			t.Fatalf("block %d (beyond lag): cold=%d hits=%d, want 50/0", b, c, h)
		}
	}
	// Lag 0: everything flushed is cold.
	z := newLagRig(0)
	z.block(t, []Op{{KeyHash: lagKey(1), Value: []byte{1}}})
	c0, _ := ReadCounters()
	z.tr.GetVia(lagKey(1), nil)
	if c1, _ := ReadCounters(); c1-c0 != 1 {
		t.Fatalf("lag 0: flushed key not cold")
	}
}

// BenchmarkEvictLag: bounded working set (8000 keys rewritten 2000/block), lag
// 0 vs 64: apply+flush+evict per block, then a read phase over the previous
// block's keys.
func BenchmarkEvictLag(b *testing.B) {
	for _, k := range []int{0, 64} {
		b.Run(map[int]string{0: "lag0", 64: "lag64"}[k], func(b *testing.B) {
			r := newLagRig(k)
			rng := rand.New(rand.NewSource(9))
			mk := func(blk uint64) []Op {
				seen := map[Hash]bool{}
				ops := make([]Op, 0, 2000)
				for len(ops) < 2000 {
					kh := lagKey(uint64(rng.Intn(8000)))
					if !seen[kh] {
						seen[kh] = true
						ops = append(ops, Op{KeyHash: kh, Value: make([]byte, 32)})
					}
				}
				sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
				return ops
			}
			tb := &testing.T{}
			for i := 0; i < 100; i++ { // warm past the lag
				r.block(tb, mk(uint64(i)))
			}
			var prev []Op
			var applyNs, readNs int64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ops := mk(uint64(i))
				t0 := nowNs()
				r.block(tb, ops)
				t1 := nowNs()
				for _, o := range prev {
					r.tr.GetVia(o.KeyHash, nil)
				}
				for j := 0; j < 4; j++ { // read-heavy: 4 passes over 2000 random keys
					for q := 0; q < 2000; q++ {
						r.tr.GetVia(lagKey(uint64(rng.Intn(8000))), nil)
					}
				}
				readNs += nowNs() - t1
				applyNs += t1 - t0
				prev = ops
			}
			b.ReportMetric(float64(applyNs)/float64(b.N)/1e6, "apply-ms/blk")
			b.ReportMetric(float64(readNs)/float64(b.N)/1e6, "read-ms/blk")
			var ms runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&ms)
			b.ReportMetric(float64(ms.HeapAlloc)/1e6, "liveheap-MB")
			b.ReportMetric(float64(r.tr.ResidentEntries()), "resident-entries")
			b.ReportMetric(float64(r.tr.ResidentTwigLeaves()), "resident-twigs")
		})
	}
}

func nowNs() int64 { return time.Now().UnixNano() }
