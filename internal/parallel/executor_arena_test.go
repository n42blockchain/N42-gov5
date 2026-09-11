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
