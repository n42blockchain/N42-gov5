// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"fmt"
	"math/rand"
	"testing"
)

type discardPutter struct{}

func (discardPutter) Put(string, []byte, []byte) error { return nil }
func (discardPutter) Delete(string, []byte) error      { return nil }

// benchForest builds a tree with n twigs: n-1 sealed and evicted (roots
// only), the youngest resident, as after a load of a long chain.
func benchForest(n int) *Tree {
	st := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(st))
	tr.SetLeafStore(LeafStoreFromGetter(st))
	tr.twigs = make([]*twig, n)
	for i := 0; i < n-1; i++ {
		tw := newBareTwig()
		tw.bits[0] = 0xff
		tw.live = 8
		tw.refreshBitsRoot()
		tr.twigs[i] = tw
	}
	tr.twigs[n-1] = newTwig()
	next := uint64(n-1) * TwigSize
	tr.nextSlot, tr.entriesBase, tr.flushedThrough, tr.evicted = next, next, next, next
	tr.rootDirty, tr.upRebuild = true, true
	tr.Root()
	return tr
}

// BenchmarkFlushTwigWalk: one block's FlushTo + EvictTwigsThrough on a
// forest of 10k / 65k / 500k twigs where ~32 sealed twigs changed (one
// deactivation each, as deaths in evicted twigs do), switch off vs on.
// The deactivations are outside the timer.
func BenchmarkFlushTwigWalk(b *testing.B) {
	for _, n := range []int{10_000, 65_000, 500_000} {
		tr := benchForest(n)
		for _, mode := range []string{"", "1"} {
			name := "off"
			if mode != "" {
				name = "on"
			}
			b.Run(fmt.Sprintf("twigs=%d/%s", n, name), func(b *testing.B) {
				defer SetFlushTwigSetForTest(mode)()
				rng := rand.New(rand.NewSource(1))
				flushed := tr.flushedThrough
				var rows, visited int
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					for k := 0; k < 32; k++ {
						id := rng.Intn(n - 1)
						tw := tr.twigs[id]
						local := uint64(rng.Intn(8))
						tr.setTwigBit(tw, id, local, !tw.bit(local))
					}
					tr.Root()
					b.StartTimer()
					next, _, err := tr.FlushTo(discardPutter{}, flushed)
					if err != nil {
						b.Fatal(err)
					}
					tr.CommitFlush()
					flushed = next
					tr.EvictTwigsThrough(flushed)
					st := tr.LastFlushStats()
					rows += st.TwigRowsN
					visited += st.TwigsVisited + tr.LastEvictStats().Visited
				}
				b.ReportMetric(float64(rows)/float64(b.N), "twigRows/op")
				b.ReportMetric(float64(visited)/float64(b.N), "visited/op")
			})
		}
	}
}
