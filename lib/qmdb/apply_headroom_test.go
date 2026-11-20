// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

// laneShape is the bench's lane block: every op rewrites a key of the hot
// set (8,000 senders and 22,857 recipients in the node).
var laneShape = applyBenchShape{name: "lane", hot: 30_858, updates: 30_858}

// TestApplyHeadroomSameTree: blocks applied on the sharded path with and
// without entry-window headroom, flushed and lag-evicted after each block
// as in the node, give the same root, undo record, resident window and
// persisted rows block by block.
func TestApplyHeadroomSameTree(t *testing.T) {
	s := laneShape
	s.hot, s.updates = 6000, 6000
	withShardApplyWorkers(8, func() {
		a, b := newABNodeIdx(t, 20_000, true), newABNodeIdx(t, 20_000, true)
		b.tr.SetApplyHeadroom(4 * s.updates)
		ra, rb := rand.New(rand.NewSource(3)), rand.New(rand.NewSource(3))
		for round := 1; round <= 14; round++ {
			opsA, opsB := a.block(ra, s, round), b.block(rb, s, round)
			a.tr.StartUndoRecording()
			b.tr.StartUndoRecording()
			a.tr.ApplyOps(opsA)
			b.tr.ApplyOps(opsB)
			ua, ub := a.tr.StopUndoRecording(), b.tr.StopUndoRecording()
			if a.tr.LastApplyPhases().Workers == 0 || b.tr.LastApplyPhases().Workers == 0 {
				t.Fatalf("round %d: not the sharded path", round)
			}
			if a.tr.Root() != b.tr.Root() {
				t.Fatalf("round %d: roots differ", round)
			}
			if !reflect.DeepEqual(ua, ub) {
				t.Fatalf("round %d: undo records differ", round)
			}
			if a.tr.ResidentEntries() != b.tr.ResidentEntries() || a.tr.NextSlot() != b.tr.NextSlot() {
				t.Fatalf("round %d: window %d/%d vs %d/%d", round, a.tr.ResidentEntries(), a.tr.NextSlot(),
					b.tr.ResidentEntries(), b.tr.NextSlot())
			}
			a.flushEvict(t)
			b.flushEvict(t)
			if !reflect.DeepEqual(a.store, b.store) {
				t.Fatalf("round %d: persisted rows differ", round)
			}
		}
	})
}

// BenchmarkApplyHeadroom: the lane block on the sharded apply (16 workers)
// over a tree flushed and evicted with the fleet's lag of 4 blocks, without
// and with 4 blocks of entry-window headroom. serGrow-ms is the window's
// growth inside the serial prep (under the root's tree lock in the node).
//
//	GOMAXPROCS=8 nice -n 19 go test -tags nosqlite,noboltdb -run NONE \
//	  -bench BenchmarkApplyHeadroom -benchtime 60x ./lib/qmdb/
func BenchmarkApplyHeadroom(b *testing.B) {
	for _, blocks := range []int{0, 4} {
		b.Run(fmt.Sprintf("headroom%d", blocks), func(b *testing.B) {
			s := laneShape
			n := newABNodeIdx(b, applyBenchSize(2_000_000), true)
			n.tr.SetApplyHeadroom(blocks * s.updates)
			rng := rand.New(rand.NewSource(7))
			round := 1
			var grow, prep, apply time.Duration
			withShardApplyWorkers(16, func() {
				for i := 0; i < 6; i++ {
					n.tr.ApplyOps(n.block(rng, s, round))
					round++
					n.flushEvict(b)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					ops := n.block(rng, s, round)
					round++
					b.StartTimer()
					n.tr.StartUndoRecording()
					t0 := time.Now()
					n.tr.ApplyOps(ops)
					apply += time.Since(t0)
					p := n.tr.LastApplyPhases()
					grow += p.SerialGrow
					prep += p.SerialPrep
					n.tr.StopUndoRecording()
					b.StopTimer()
					n.flushEvict(b)
					b.StartTimer()
				}
			})
			per := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / float64(b.N) / 1e6 }
			b.ReportMetric(per(apply), "apply-ms")
			b.ReportMetric(per(prep), "serPrep-ms")
			b.ReportMetric(per(grow), "serGrow-ms")
		})
	}
}
