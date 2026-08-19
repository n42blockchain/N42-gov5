// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// LoadFrom is the boot path: a node rebuilds its in-RAM index from the entry
// log every start, because the index is not persisted. Profiling a live node
// showed that single call allocating 14.7 GB, of which two thirds was garbage
// by construction — every sealed twig took a 128 KiB node heap it freed again
// a few lines later, plus a 64 KiB expanded-leaf temporary it copied out of
// and dropped. This benchmark exists so the per-twig cost stays visible.

package qmdb

import "testing"

// buildForest fills a store with enough slots to span several twigs.
func buildForest(tb testing.TB, twigs int) (mapStore, *Tree) {
	tb.Helper()
	store := newMapStore()
	tr := New()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))

	var flushed uint64
	const perBlock = 500
	blocks := uint64(twigs*TwigSize/perBlock) + 1
	for b := uint64(0); b < blocks; b++ {
		rvApply(tr, rvBlockOps(b, perBlock))
		next, _, err := tr.FlushTo(store, flushed)
		if err != nil {
			tb.Fatalf("flush: %v", err)
		}
		tr.EvictThrough(next)
		flushed = next
	}
	return store, tr
}

func BenchmarkLoadFromForest(b *testing.B) {
	store, tr := buildForest(b, 8)
	want := tr.Root()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		re := New()
		re.SetCold(ColdReaderFromGetter(store))
		re.SetLeafStore(LeafStoreFromGetter(store))
		if err := re.LoadFrom(store); err != nil {
			b.Fatalf("LoadFrom: %v", err)
		}
		if got := re.Root(); got != want {
			b.Fatalf("reloaded root %x want %x", got[:8], want[:8])
		}
	}
}

// TestLoadFromScratchHeapKeepsRoot pins the reuse itself: a forest reloaded
// with the shared scratch node heap must produce the identical root, and the
// twigs must not end up aliasing the shared buffer (which would make every
// sealed twig report the last-loaded twig's leaves).
func TestLoadFromScratchHeapKeepsRoot(t *testing.T) {
	store, tr := buildForest(t, 4)
	want := tr.Root()

	re := New()
	re.SetCold(ColdReaderFromGetter(store))
	re.SetLeafStore(LeafStoreFromGetter(store))
	if err := re.LoadFrom(store); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got := re.Root(); got != want {
		t.Fatalf("reloaded root %x want %x", got[:8], want[:8])
	}
	// Every sealed twig must have released its array; a lingering pointer is
	// exactly how a shared buffer would leak into the next twig.
	active := int((re.NextSlot() - 1) / TwigSize)
	for id, tw := range re.twigs {
		if tw == nil || id >= active {
			continue
		}
		if tw.nodes != nil {
			t.Fatalf("sealed twig %d still holds a node heap (would alias the shared scratch)", id)
		}
	}
}
