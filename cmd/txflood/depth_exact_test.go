package main

import (
	"fmt"
	"testing"
)

// TestSampleDepthExactUniform: every sampled sender has the same shortfall,
// so the mean equals that shortfall exactly and the extrapolation to the
// whole fleet is exact too.
func TestSampleDepthExactUniform(t *testing.T) {
	total := 1000
	sample := []int{3, 17, 402, 999}
	next := func(s int) int64 { return 1050 } // base 1000, 50 in flight
	chain := func(s int) (uint64, error) { return 1000, nil }

	depth, sampled := sampleDepthExact(sample, next, chain, total)
	if sampled != len(sample) {
		t.Fatalf("sampled = %d, want %d", sampled, len(sample))
	}
	if want := int64(50 * total); depth != want {
		t.Fatalf("depth = %d, want %d", depth, want)
	}
}

// TestSampleDepthExactSkewed: half the sampled senders are loaded, half are
// idle. With a full sample (sample size == total) the mean*total reduces to
// the exact sum, so this also checks the extrapolation introduces no bias
// when there is nothing to extrapolate.
func TestSampleDepthExactSkewed(t *testing.T) {
	total := 10
	sample := make([]int, total)
	for i := range sample {
		sample[i] = i
	}
	next := func(s int) int64 {
		if s < 5 {
			return 100 // 100 in flight
		}
		return 0
	}
	chain := func(s int) (uint64, error) { return 0, nil }

	depth, sampled := sampleDepthExact(sample, next, chain, total)
	if sampled != total {
		t.Fatalf("sampled = %d, want %d", sampled, total)
	}
	if depth != 500 { // exact sum: 5 senders x 100
		t.Fatalf("depth = %d, want 500", depth)
	}
}

// TestSampleDepthExactNotStarted: a sender the generator has not reached yet
// has nextNonceToSend == its own starting nonce, and the chain reports that
// same starting nonce back (nothing of theirs has been mined because nothing
// of theirs has been sent) -- the shortfall must be zero, not the sender's
// whole starting nonce.
func TestSampleDepthExactNotStarted(t *testing.T) {
	total := 3
	sample := []int{0, 1, 2}
	base := []uint64{1000, 2000, 3000}
	next := func(s int) int64 { return int64(base[s]) }
	chain := func(s int) (uint64, error) { return base[s], nil }

	depth, sampled := sampleDepthExact(sample, next, chain, total)
	if sampled != total {
		t.Fatalf("sampled = %d, want %d", sampled, total)
	}
	if depth != 0 {
		t.Fatalf("depth = %d, want 0 for senders that have not started", depth)
	}
}

// TestSampleDepthExactSkipsErrors: a sender the fake nonce source cannot
// answer this tick is left out of both the numerator and the denominator --
// it must not be treated as zero (which would silently pull the mean down)
// or as fully in flight (which would pull it up).
func TestSampleDepthExactSkipsErrors(t *testing.T) {
	next := func(s int) int64 { return 100 }
	chain := func(s int) (uint64, error) {
		if s == 0 {
			return 0, fmt.Errorf("boom")
		}
		return 0, nil
	}

	depth, sampled := sampleDepthExact([]int{0, 1}, next, chain, 2)
	if sampled != 1 {
		t.Fatalf("sampled = %d, want 1", sampled)
	}
	if depth != 200 { // mean=100 over the one answered sender, x 2 total senders
		t.Fatalf("depth = %d, want 200", depth)
	}
}

// TestSampleDepthExactAllFail: nothing in the sample answers -- depth must
// read 0 sampled so the caller keeps the previous (rotating) estimate rather
// than injecting a bogus depth of 0.
func TestSampleDepthExactAllFail(t *testing.T) {
	next := func(s int) int64 { return 100 }
	chain := func(s int) (uint64, error) { return 0, fmt.Errorf("down") }

	_, sampled := sampleDepthExact([]int{0, 1, 2}, next, chain, 3)
	if sampled != 0 {
		t.Fatalf("sampled = %d, want 0", sampled)
	}
}

// TestNextSampleSweepsExactlyOnce: with total=1000 and size=100, a full
// sweep is 10 ticks and must cover every sender exactly once, with the
// cursor wrapping back to where it started.
func TestNextSampleSweepsExactlyOnce(t *testing.T) {
	total, size := 1000, 100
	cursor := 0
	seen := make(map[int]int, total)
	for tick := 0; tick < 10; tick++ {
		var sample []int
		sample, cursor = nextSample(cursor, total, size)
		if len(sample) != size {
			t.Fatalf("tick %d: sample size = %d, want %d", tick, len(sample), size)
		}
		for _, s := range sample {
			seen[s]++
		}
	}
	if len(seen) != total {
		t.Fatalf("sweep covered %d of %d senders", len(seen), total)
	}
	for s, n := range seen {
		if n != 1 {
			t.Fatalf("sender %d sampled %d times in one sweep", s, n)
		}
	}
	if cursor != 0 {
		t.Fatalf("cursor after an exact sweep = %d, want 0", cursor)
	}
}

// TestNextSampleClipsToTotal: a fleet smaller than the requested sample size
// must not repeat a sender within one call.
func TestNextSampleClipsToTotal(t *testing.T) {
	sample, cursor := nextSample(0, 3, 100)
	if len(sample) != 3 {
		t.Fatalf("sample size = %d, want 3", len(sample))
	}
	seen := map[int]bool{}
	for _, s := range sample {
		if seen[s] {
			t.Fatalf("sender %d repeated in one sample", s)
		}
		seen[s] = true
	}
	if cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (wraps immediately since sample == total)", cursor)
	}
}

// TestNextSampleEmptyFleet: zero senders must not panic or divide by zero.
func TestNextSampleEmptyFleet(t *testing.T) {
	sample, cursor := nextSample(5, 0, 100)
	if sample != nil {
		t.Fatalf("sample = %v, want nil", sample)
	}
	if cursor != 5 {
		t.Fatalf("cursor = %d, want unchanged 5", cursor)
	}
}
