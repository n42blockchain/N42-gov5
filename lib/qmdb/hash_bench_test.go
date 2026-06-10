package qmdb

import "testing"

// BenchmarkHashNode guards the alloc-free hot path: hashNode must do 0 allocs
// (the previous blake3.Sum256(buf[:]) form escaped buf to the heap on every call,
// allocating ~190 GB over a full conversion and driving a third of CPU into GC).
func BenchmarkHashNode(b *testing.B) {
	var l, r Hash
	for i := range l {
		l[i] = byte(i)
		r[i] = byte(255 - i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	var sink Hash
	for i := 0; i < b.N; i++ {
		sink = hashNode(l, r)
	}
	_ = sink
}

// BenchmarkTwigRecompute measures a full 2048-leaf twig rebuild (4095 hashNode
// calls) — now only paid on hydration/bulk-load, not per leaf change.
func BenchmarkTwigRecompute(b *testing.B) {
	tw := newTwig()
	for i := 0; i < TwigSize; i++ {
		var h Hash
		h[0] = byte(i)
		h[1] = byte(i >> 8)
		tw.nodes[TwigSize+i] = h
	}
	tw.dirty = true
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tw.recompute()
	}
}

// BenchmarkTwigSetLeaf measures the eager O(log) path-update (11 hashNode calls)
// that replaced the full recompute on the steady-state Set/deactivate path.
func BenchmarkTwigSetLeaf(b *testing.B) {
	tw := newTwig()
	var h Hash
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h[0], h[1], h[2] = byte(i), byte(i>>8), byte(i>>16)
		tw.setLeaf(uint64(i%TwigSize), h)
	}
}
