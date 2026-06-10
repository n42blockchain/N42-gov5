// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package blspool

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestCommitteeScratchBitIdentical: the scratch-reusing sampler must produce
// EXACTLY the same committees as the one-shot form across views/hashes/pool
// sizes, including back-to-back reuse of the same scratch (committee selection
// feeds chain content — any divergence changes the chain).
func TestCommitteeScratchBitIdentical(t *testing.T) {
	cs := NewCommitteeScratch()
	for view := uint64(0); view < 50; view++ {
		var h types.Hash
		h[0], h[7], h[31] = byte(view), byte(view*7), byte(view*13)
		for _, active := range []int{512, 513, 1000, 200000} {
			fresh := NewCommitteeScratch().Committee(view, h, active, 512)
			reused := cs.Committee(view, h, active, 512) // same scratch, many calls
			if len(fresh) != len(reused) {
				t.Fatalf("view %d active %d: len %d vs %d", view, active, len(fresh), len(reused))
			}
			for i := range fresh {
				if fresh[i] != reused[i] {
					t.Fatalf("view %d active %d idx %d: %d vs %d", view, active, i, fresh[i], reused[i])
				}
			}
		}
	}
}

// BenchmarkCommitteeScratch guards the steady-state allocation count (the
// one-shot form allocated ~100KB/call = 49% of conversion allocations).
func BenchmarkCommitteeScratch(b *testing.B) {
	cs := NewCommitteeScratch()
	var h types.Hash
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h[0] = byte(i)
		_ = cs.Committee(uint64(i), h, 200000, 512)
	}
}
