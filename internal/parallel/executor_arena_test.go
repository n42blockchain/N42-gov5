package parallel

import (
	"errors"
	"testing"
)

var errTest = errors.New("test failure")

// A released executor's per-transaction arrays come back to the next
// executor of the same size, and the reuse must not leak the previous run's
// status, incarnations, results or read/write sets into the new one.
func TestExecutorArenaReuseIsClean(t *testing.T) {
	run := func(seed byte) []TxResult {
		exec := NewExecutor(64, 4, func(txIndex int, rw *ReadWriteSet) error {
			rw.RecordWrite(balanceKey(byte(txIndex)), []byte{seed, byte(txIndex)})
			if txIndex == 7 && seed == 1 {
				return errTest
			}
			return nil
		})
		results := exec.Run()
		out := make([]TxResult, len(results))
		copy(out, results)
		for i := range exec.rwSets {
			if len(exec.rwSets[i].Writes) != 1 {
				t.Fatalf("seed %d: tx %d recorded %d writes, want 1", seed, i, len(exec.rwSets[i].Writes))
			}
		}
		exec.Release()
		return out
	}
	first := run(1)
	if first[7].Err == nil {
		t.Fatal("first run: tx 7 should have failed")
	}
	second := run(2)
	for i, r := range second {
		if r.Err != nil {
			t.Fatalf("second run reused the first run's result at tx %d: %v", i, r.Err)
		}
	}
	// Different sizes must not share a too-small arena.
	big := NewExecutor(1000, 4, func(txIndex int, rw *ReadWriteSet) error { return nil })
	if len(big.rwSets) != 1000 || len(big.results) != 1000 {
		t.Fatalf("arena sized %d/%d for 1000 transactions", len(big.rwSets), len(big.results))
	}
	big.Release()
}

// The free list must converge on the largest blocks: after two small
// executors were released (a leg's ramp), a run of larger ones must end up
// reusing an arena rather than allocating every set fresh.
func TestExecutorArenaFreeListKeepsTheLargest(t *testing.T) {
	arenaFree.mu.Lock()
	arenaFree.list = nil
	arenaFree.mu.Unlock()
	noop := func(int, *ReadWriteSet) error { return nil }
	// Two small executors alive at once (the builder and the import of a
	// leader), released together: both arenas sit in the list.
	smallA, smallB := NewExecutor(16, 2, noop), NewExecutor(16, 2, noop)
	smallA.Run()
	smallB.Run()
	smallA.Release()
	smallB.Release()
	// Two big executors in a row: the first drops a small arena and builds
	// a large one; its Release must displace a small one from the list.
	first := NewExecutor(4096, 4, noop)
	first.Run()
	firstSets := first.rwSets[0]
	first.Release()
	second := NewExecutor(4096, 4, noop)
	if second.rwSets[0] != firstSets {
		t.Fatalf("second 4096-transaction executor did not reuse the first one's arena")
	}
	second.Run()
	second.Release()
	arenaFree.mu.Lock()
	caps := []int{}
	for _, a := range arenaFree.list {
		caps = append(caps, cap(a.rwSets))
	}
	arenaFree.mu.Unlock()
	big := 0
	for _, c := range caps {
		if c >= 4096 {
			big++
		}
	}
	if big == 0 {
		t.Fatalf("free list after the big blocks: caps %v, want one of at least 4096", caps)
	}
}
