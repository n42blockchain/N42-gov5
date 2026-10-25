// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"testing"
)

// TestSyscallFinalizeParallelMatchesSerial: under N42_SYSCALL_FINALIZE_ONCE
// FinalizeTx's no-op walk (finalizeNoopParallel, taken by every per-tx and
// block-end finalize of runFFBlock once the threshold is 1) leaves the same
// object flags, stateObjectsDirty and journal dirty set, the same root
// dirty set, post-state, read order and CommitBlock rows as the serial
// walk, with and without detached credits, also combined with
// N42_FINALIZE_FAST.
func TestSyscallFinalizeParallelMatchesSerial(t *testing.T) {
	type size struct{ accts, ops int }
	sizes := []size{{60, 200}, {400, 2000}}
	if !testing.Short() {
		sizes = append(sizes, size{9000, 30000})
	}
	for _, sz := range sizes {
		for seed := int64(1); seed <= 6; seed++ {
			for _, detach := range []bool{false, true} {
				for _, fast := range []bool{false, true} {
					tag := fmt.Sprintf("accts=%d seed=%d detach=%v fast=%v", sz.accts, seed, detach, fast)
					restoreFast := SetFinalizeFastForTest(fast)
					restore := SetSyscallFinalizeOnceForTest(false)
					want := runFFBlock(t, seed, sz.accts, sz.ops, detach)
					restore()
					for _, min := range []int{1, 2048} {
						restoreMin := SetSyscallFinalizeParMinForTest(min)
						restore := SetSyscallFinalizeOnceForTest(true)
						w0 := SyscallFinalizeParWalks()
						got := runFFBlock(t, seed, sz.accts, sz.ops, detach)
						if min == 1 && SyscallFinalizeParWalks() == w0 {
							t.Fatalf("%s: the parallel walk did not run", tag)
						}
						restore()
						restoreMin()
						requireSameFFBlock(t, fmt.Sprintf("%s min=%d", tag, min), want, got)
					}
					restoreFast()
				}
			}
		}
	}
}
