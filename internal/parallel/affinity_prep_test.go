package parallel

import (
	"reflect"
	"sync/atomic"
	"testing"
)

// TestAdoptAffinityMatchesPrepare: a partition computed by
// PrepareAffinityFor and adopted is the one PrepareAffinity computes, in
// every wave mode, and a run on it executes every transaction once.
func TestAdoptAffinityMatchesPrepare(t *testing.T) {
	const numTxs, senders, workers = 20000, 700, 8
	key := func(i int) uint64 { return uint64(i*i%senders)*0x9e3779b97f4a7c15 + 1 }
	for _, m := range []struct{ lpt, steal bool }{{false, false}, {true, false}, {true, true}} {
		withWaveModes(t, m.lpt, m.steal)
		inline := NewExecutor(numTxs, workers, nil)
		inline.SetAffinity(key)
		inline.PrepareAffinity()

		prep := PrepareAffinityFor(numTxs, workers, key, 3)
		var runs [numTxs]atomic.Int32
		adopted := NewExecutor(numTxs, workers, func(i int, rw *ReadWriteSet) error {
			runs[i].Add(1)
			rw.RecordWrite(LocationKey{Address: benchAddr(i), Field: FieldBalance}, benchVal(uint64(i)))
			return nil
		})
		adopted.SetAffinity(key)
		if !adopted.AdoptAffinity(prep) {
			t.Fatalf("lpt=%v steal=%v: a matching preparation was refused", m.lpt, m.steal)
		}
		if !reflect.DeepEqual(inline.preKeys, adopted.preKeys) {
			t.Fatalf("lpt=%v steal=%v: keys differ", m.lpt, m.steal)
		}
		if !reflect.DeepEqual(inline.prePart, adopted.prePart) {
			t.Fatalf("lpt=%v steal=%v: partition differs", m.lpt, m.steal)
		}
		if adopted.AdoptAffinity(prep) {
			t.Fatal("a preparation was adopted twice")
		}
		adopted.Run()
		for i := range runs {
			if runs[i].Load() != 1 {
				t.Fatalf("tx %d ran %d times", i, runs[i].Load())
			}
		}
		inline.Release()
		adopted.Release()
	}
}

// TestAdoptAffinityRefusesMismatch: a preparation for another transaction
// count, worker count or wave mode is refused, and so is any without an
// affinity function; the executor then prepares nothing.
func TestAdoptAffinityRefusesMismatch(t *testing.T) {
	withWaveModes(t, false, false)
	key := func(i int) uint64 { return uint64(i % 37) }
	fresh := func(n, w int) *Executor {
		e := NewExecutor(n, w, nil)
		e.SetAffinity(key)
		return e
	}
	if fresh(1000, 8).AdoptAffinity(PrepareAffinityFor(999, 8, key, 2)) {
		t.Fatal("adopted a preparation for another transaction count")
	}
	if fresh(1000, 8).AdoptAffinity(PrepareAffinityFor(1000, 4, key, 2)) {
		t.Fatal("adopted a preparation for another worker count")
	}
	// Requested counts resolve the same way (capped at the transactions).
	if !fresh(6, 32).AdoptAffinity(PrepareAffinityFor(6, 64, key, 1)) {
		t.Fatal("refused a preparation whose resolved worker count matches")
	}
	lpt := PrepareAffinityFor(1000, 8, key, 2)
	withWaveModes(t, true, false)
	e := fresh(1000, 8)
	if e.AdoptAffinity(lpt) {
		t.Fatal("adopted a preparation computed under another wave mode")
	}
	if k, q := e.PreparedAffinityForTest(); k != nil || q != nil {
		t.Fatal("a refused preparation left keys behind")
	}
	if NewExecutor(1000, 8, nil).AdoptAffinity(PrepareAffinityFor(1000, 8, key, 2)) {
		t.Fatal("adopted a preparation without an affinity function")
	}
	if PrepareAffinityFor(2, 8, key, 1) != nil || fresh(3, 8).AdoptAffinity(nil) {
		t.Fatal("a block PrepareAffinity skips got a preparation")
	}
}
