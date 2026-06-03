package ethel

import (
	"math/rand"
	"testing"
)

// drainInOrder mimics the aggregator: put a result, then drain contiguous
// blocks from `next`, asserting strictly increasing block order.
func TestReorderBufferInOrderDrain(t *testing.T) {
	const n = 5000
	rb := newReorderBuffer(512)
	// Shuffle [0,n) but keep the shuffle window bounded (~300) so it never
	// exceeds the ring — mimics a bounded look-ahead with out-of-order finish.
	order := make([]uint64, n)
	for i := range order {
		order[i] = uint64(i)
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < n; i++ {
		j := i + rng.Intn(300)
		if j >= n {
			j = n - 1
		}
		order[i], order[j] = order[j], order[i]
	}

	next := uint64(0)
	emitted := make([]uint64, 0, n)
	for _, blk := range order {
		rb.put(WitnessResult{BlockNum: blk})
		for {
			r, ok := rb.take(next)
			if !ok {
				break
			}
			emitted = append(emitted, r.BlockNum)
			next++
		}
	}
	// Drain any tail.
	for {
		r, ok := rb.take(next)
		if !ok {
			break
		}
		emitted = append(emitted, r.BlockNum)
		next++
	}

	if len(emitted) != n {
		t.Fatalf("emitted %d != %d", len(emitted), n)
	}
	for i, b := range emitted {
		if b != uint64(i) {
			t.Fatalf("out of order at %d: got block %d", i, b)
		}
	}
	if rb.len() != 0 {
		t.Errorf("buffer not empty: %d", rb.len())
	}
}

// A result arriving more than `size` ahead of the head must still be emitted in
// order via the overflow safety net (window deliberately tiny here).
func TestReorderBufferOverflowSafety(t *testing.T) {
	rb := newReorderBuffer(4)
	// Put blocks 0, then 10 (10 % 4 == 2; 0 % 4 == 0 — no collision yet), then
	// 6 (6%4==2) which collides with 10's slot -> overflow.
	rb.put(WitnessResult{BlockNum: 0})
	rb.put(WitnessResult{BlockNum: 10})
	rb.put(WitnessResult{BlockNum: 6})
	if rb.overflowLen() == 0 {
		t.Fatal("expected an overflow entry for the colliding far-ahead block")
	}
	// Fill the gaps and drain 0..10 in order.
	for _, b := range []uint64{1, 2, 3, 4, 5, 7, 8, 9} {
		rb.put(WitnessResult{BlockNum: b})
	}
	next := uint64(0)
	for next <= 10 {
		r, ok := rb.take(next)
		if !ok {
			t.Fatalf("missing block %d", next)
		}
		if r.BlockNum != next {
			t.Fatalf("got %d want %d", r.BlockNum, next)
		}
		next++
	}
	if rb.len() != 0 || rb.overflowLen() != 0 {
		t.Errorf("not fully drained: len=%d overflow=%d", rb.len(), rb.overflowLen())
	}
}

// take must zero the slot so the held blob slices are released for GC.
func TestReorderBufferReleasesBlobs(t *testing.T) {
	rb := newReorderBuffer(8)
	rb.put(WitnessResult{BlockNum: 0, AcctCSBytes: make([]byte, 1024)})
	r, ok := rb.take(0)
	if !ok || len(r.AcctCSBytes) != 1024 {
		t.Fatal("take returned wrong data")
	}
	if rb.ring[0].AcctCSBytes != nil {
		t.Error("slot not zeroed after take — blob still referenced")
	}
}
