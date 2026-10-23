// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// applyBenchShape is one follower-block workload for BenchmarkShardApply.
type applyBenchShape struct {
	name    string
	live    int  // live keys in the base tree
	hot     int  // keys rewritten every block (0 = draw updates from all keys)
	updates int  // account-sized updates per block
	words   int  // 32-byte storage-word updates per block
	inserts int  // fresh keys per block
	deletes int  // deletes per block
	scatter bool // updates hit random old keys across the whole history
}

// The qs bench block (~163k transfers) is ~31k ops: ~22k account updates
// whose previous versions were written one block earlier (the tx corpus is
// reused every block), some storage words, a few inserts and deletes. The
// "scatter" shape draws the same op mix from the whole key space instead, so
// every deactivation lands in an old, evicted twig.
var applyBenchShapes = []applyBenchShape{
	{name: "hot", live: 6_000_000, hot: 32_000, updates: 22_000, words: 8_000, inserts: 900, deletes: 100},
	{name: "scatter", live: 6_000_000, updates: 22_000, words: 8_000, inserts: 900, deletes: 100, scatter: true},
}

func applyBenchSize(def int) int {
	if n, err := strconv.Atoi(os.Getenv("QMDB_APPLY_BENCH_LIVE")); err == nil && n > 0 {
		return n
	}
	return def
}

// abKey derives distinct key hashes cheaply (a uniform digest is all the
// index and the sort need; blake3 per key would dominate the base build).
func abKey(i uint64) Hash {
	var h Hash
	x := i*0x9E3779B97F4A7C15 + 0x632BE59BD9B4E019
	for j := 0; j < 4; j++ {
		x ^= x >> 31
		x *= 0xBF58476D1CE4E5B9
		x ^= x >> 29
		binary.LittleEndian.PutUint64(h[8*j:], x)
		x += i + uint64(j)
	}
	return h
}

func abVal(n int, k uint64, round int) []byte {
	v := make([]byte, n)
	binary.BigEndian.PutUint64(v[n-8:], k*1_000_003+uint64(round))
	v[0] |= 1
	return v
}

func sortOps(ops []Op) {
	sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
}

// abNode is a tree with an in-RAM map index (the node's shape after
// LoadFrom), a map cold store and the fleet's evict lag of 4 blocks.
type abNode struct {
	tr      *Tree
	store   mapStore
	flushed uint64
	lag     EvictLag
	next    uint64 // next fresh key id
}

func (n *abNode) flushEvict(tb testing.TB) {
	var err error
	if n.flushed, _, err = n.tr.FlushTo(n.store, n.flushed); err != nil {
		tb.Fatal(err)
	}
	n.lag.Evict(n.tr, n.flushed, n.flushed)
}

func newABNode(tb testing.TB, live int) *abNode { return newABNodeIdx(tb, live, false) }

// newABNodeIdx is newABNode with the sharded index (what NewIndexFor builds
// under N42_QMDB_SHARD_APPLY) instead of the map.
func newABNodeIdx(tb testing.TB, live int, sharded bool) *abNode {
	n := &abNode{tr: New(), store: newMapStore(), lag: EvictLag{K: 4}}
	if sharded {
		n.tr.SetIndex(newShardedMapIndex(live + live/8))
	} else {
		n.tr.SetIndex(newMapIndexSized(live + live/8))
	}
	n.tr.SetCold(ColdReaderFromGetter(n.store))
	n.tr.SetLeafStore(LeafStoreFromGetter(n.store))
	const step = 250_000
	for lo := 0; lo < live; lo += step {
		hi := min(lo+step, live)
		ops := make([]Op, 0, hi-lo)
		for k := lo; k < hi; k++ {
			ops = append(ops, Op{KeyHash: abKey(uint64(k)), Value: abVal(32, uint64(k), 0)})
		}
		sortOps(ops)
		n.tr.ApplyOps(ops)
		n.flushEvict(tb)
	}
	n.next = uint64(live)
	return n
}

// block builds one sorted, key-distinct block of the shape.
func (n *abNode) block(rng *rand.Rand, s applyBenchShape, round int) []Op {
	seen := make(map[uint64]bool, s.updates+s.words+s.inserts+s.deletes)
	ops := make([]Op, 0, s.updates+s.words+s.inserts+s.deletes)
	pick := func() uint64 {
		for {
			var k uint64
			if s.scatter || s.hot == 0 {
				k = uint64(rng.Int63n(int64(n.next)))
			} else {
				k = uint64(rng.Intn(s.hot))
			}
			if !seen[k] {
				seen[k] = true
				return k
			}
		}
	}
	for i := 0; i < s.updates; i++ {
		k := pick()
		ops = append(ops, Op{KeyHash: abKey(k), Value: abVal(20, k, round)})
	}
	for i := 0; i < s.words; i++ {
		k := pick()
		ops = append(ops, Op{KeyHash: abKey(k), Value: abVal(32, k, round)})
	}
	for i := 0; i < s.deletes; i++ {
		ops = append(ops, Op{KeyHash: abKey(pick())})
	}
	for i := 0; i < s.inserts; i++ {
		k := n.next
		n.next++
		seen[k] = true
		ops = append(ops, Op{KeyHash: abKey(k), Value: abVal(32, k, round)})
	}
	sortOps(ops)
	return ops
}

// BenchmarkShardApply measures Tree.ApplyOps on a follower-sized block over
// a multi-million-key tree with undo recording on, flushing and lag-evicting
// after every block like the node does. Sub-benchmarks: shape x ShardApply
// worker count (0 = the sequential path). QMDB_APPLY_BENCH_LIVE overrides the
// base key count.
func BenchmarkShardApply(b *testing.B) {
	for _, s := range applyBenchShapes {
		s.live = applyBenchSize(s.live)
		for _, sharded := range []bool{false, true} {
			// The map index serves the existing paths (w <= 0), the sharded
			// index the sharded apply (w > 0), as in the node.
			var ws []int
			for _, w := range applyBenchWorkers() {
				if (w > 0) == sharded {
					ws = append(ws, w)
				}
			}
			if len(ws) > 0 {
				benchShardApplyShape(b, s, sharded, ws)
			}
		}
	}
}

func benchShardApplyShape(b *testing.B, s applyBenchShape, sharded bool, workers []int) {
	{
		n := newABNodeIdx(b, s.live, sharded)
		rng := rand.New(rand.NewSource(7))
		round := 1
		// Warm-up: give the hot set its one-block-old history.
		for i := 0; i < 6; i++ {
			n.tr.ApplyOps(n.block(rng, s, round))
			round++
			n.flushEvict(b)
		}
		for _, w := range workers {
			b.Run(fmt.Sprintf("%s/w%d", s.name, w), func(b *testing.B) {
				var total time.Duration
				var ph ApplyPhases
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					ops := n.block(rng, s, round)
					round++
					b.StartTimer()
					n.tr.StartUndoRecording()
					t0 := time.Now()
					applyWith(w, n.tr, ops)
					total += time.Since(t0)
					p := n.tr.LastApplyPhases()
					ph.Probe += p.Probe
					ph.Serial += p.Serial
					ph.Commit += p.Commit
					ph.Fold += p.Fold
					n.tr.StopUndoRecording()
					b.StopTimer()
					n.flushEvict(b)
					b.StartTimer()
				}
				per := func(d time.Duration) float64 { return float64(d.Microseconds()) / float64(b.N) / 1000 }
				b.ReportMetric(per(total), "apply-ms/block")
				if w > 0 {
					b.ReportMetric(per(ph.Probe), "probe-ms")
					b.ReportMetric(per(ph.Serial), "serial-ms")
					b.ReportMetric(per(ph.Commit), "commit-ms")
					b.ReportMetric(per(ph.Fold), "fold-ms")
				}
			})
		}
	}
}

// applyBenchWorkers lists the sub-benchmark worker counts: ShardApply counts,
// and negative values for the S72 parallel pre-pass (N42_QMDB_PARALLEL_APPLY)
// at that many workers, for comparison.
func applyBenchWorkers() []int {
	if v := os.Getenv("QMDB_APPLY_BENCH_WORKERS"); v != "" {
		var ws []int
		for _, f := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(f); err == nil {
				ws = append(ws, n)
			}
		}
		return ws
	}
	return []int{0, -8, 4, 8, 16, 32}
}

func applyWith(w int, tr *Tree, ops []Op) {
	if w < 0 {
		withApplyWorkers(-w, func() { tr.ApplyOps(ops) })
		return
	}
	withShardApplyWorkers(w, func() { tr.ApplyOps(ops) })
}

// applyPassTimes is the sequential apply split into passes, one per kind of
// work, timed separately. With distinct keys the passes leave the same tree
// as ApplyOps (only the order of independent mutations differs), which is
// all a cost breakdown needs.
type applyPassTimes struct {
	probe, undo, deact, slots, put, leaf, fold, root time.Duration
	cold                                             int
}

func (t *Tree) applyOpsPhased(ops []Op, ph *applyPassTimes) {
	pre := make([]preOp, len(ops))
	t0 := time.Now()
	for i := range ops {
		pre[i].old, pre[i].hasOld = t.idx.Get(ops[i].KeyHash)
	}
	t1 := time.Now()
	ph.probe += t1.Sub(t0)
	for i := range ops {
		if p := &pre[i]; p.hasOld {
			if p.old < t.entriesBase {
				ph.cold++
			}
			t.recordDeactivation(p.old, ops[i].KeyHash)
		}
	}
	t2 := time.Now()
	ph.undo += t2.Sub(t1)
	t.BeginLeafBatch()
	for i := range ops {
		if pre[i].hasOld {
			t.deactivate(pre[i].old)
		}
	}
	t3 := time.Now()
	ph.deact += t3.Sub(t2)
	for i := range ops {
		kh, v := ops[i].KeyHash, ops[i].Value
		if len(v) == 0 {
			continue
		}
		slot := t.nextSlot
		t.nextSlot++
		if t.rec != nil {
			t.rec.AppendedKeys = append(t.rec.AppendedKeys, kh)
		}
		pre[i].old = slot
		tw := t.twigFor(slot)
		local := slot % TwigSize
		av := t.allocVal(v)
		t.setEntry(slot, entry{keyHash: kh, value: av, active: true})
		t.writeLeafEntry(tw, int(slot/TwigSize), local, slot, kh, av)
		t.setTwigBit(tw, int(slot/TwigSize), local, true)
		tw.live++
		t.markUpperDirty(int(slot / TwigSize))
		t.rootDirty = true
	}
	t4 := time.Now()
	ph.slots += t4.Sub(t3)
	for i := range ops {
		if len(ops[i].Value) == 0 {
			if pre[i].hasOld {
				t.idx.Delete(ops[i].KeyHash)
			}
			continue
		}
		t.idx.Put(ops[i].KeyHash, pre[i].old)
	}
	t5 := time.Now()
	ph.put += t5.Sub(t4)
	t.flushLeafJobs()
	t6 := time.Now()
	ph.leaf += t6.Sub(t5)
	t.EndLeafBatch()
	t7 := time.Now()
	ph.fold += t7.Sub(t6)
	t.Root()
	ph.root += time.Since(t7)
}

// TestShardApplyPhases prints the per-pass cost split of a follower block
// (QMDB_APPLY_PHASES=1; slow: builds a multi-million-key base tree).
func TestShardApplyPhases(t *testing.T) {
	if os.Getenv("QMDB_APPLY_PHASES") == "" {
		t.Skip("set QMDB_APPLY_PHASES=1")
	}
	const blocks = 20
	for _, s := range applyBenchShapes {
		s.live = applyBenchSize(s.live)
		n := newABNode(t, s.live)
		rng := rand.New(rand.NewSource(7))
		round := 1
		for i := 0; i < 6; i++ {
			n.tr.ApplyOps(n.block(rng, s, round))
			round++
			n.flushEvict(t)
		}
		var ph applyPassTimes
		var seq time.Duration
		if f := os.Getenv("QMDB_APPLY_PROF"); f != "" {
			out, err := os.Create(f + "." + s.name)
			if err != nil {
				t.Fatal(err)
			}
			_ = pprof.StartCPUProfile(out)
		}
		for i := 0; i < blocks; i++ {
			ops := n.block(rng, s, round)
			round++
			n.tr.StartUndoRecording()
			t0 := time.Now()
			if i%2 == 0 {
				n.tr.applyOpsPhased(ops, &ph)
			} else {
				n.tr.ApplyOps(ops)
				seq += time.Since(t0)
			}
			n.tr.StopUndoRecording()
			n.flushEvict(t)
		}
		pprof.StopCPUProfile()
		ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 / (blocks / 2) }
		t.Logf("%s: ApplyOps %.2f ms/block; passes probe %.2f undo %.2f (cold %d/block) deact %.2f slots %.2f put %.2f leaf %.2f fold %.2f root %.2f",
			s.name, ms(seq), ms(ph.probe), ms(ph.undo), ph.cold/(blocks/2), ms(ph.deact), ms(ph.slots), ms(ph.put), ms(ph.leaf), ms(ph.fold), ms(ph.root))
	}
}
