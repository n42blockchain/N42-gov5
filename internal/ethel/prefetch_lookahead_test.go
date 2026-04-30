// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// TestPrefetcher_LookaheadEnqueue_NoDuplicates simulates the executor's
// 2-block lookahead pattern (executor.go: prime N+1 on first iter, then
// always enqueue N+2) and confirms the prefetcher's blockCh never
// contains a duplicate block.
//
// Without the "first-iter prime + N+2-only afterwards" dedup, every
// iteration would enqueue both N+1 and N+2, where N+1 was already in
// the queue from the previous iteration's N+2 — wasting a channel slot
// and a doFetch call (auto-aborted via cancelled() but not free).
func TestPrefetcher_LookaheadEnqueue_NoDuplicates(t *testing.T) {
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	buf := state.NewPlainStateBuffer()
	var current atomic.Uint64
	p := newPrefetcher(context.Background(), nil, asRoDB{db}, buf, nil, nil, nil, nil, &current, false)

	// Drive the same lookahead pattern executor.go uses, WITHOUT
	// starting the prefetcher's loop — the channel state is what we
	// inspect.
	const startBlock = uint64(1000)
	const endBlock = uint64(1010)
	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		current.Store(blockNum)
		if blockNum == startBlock && blockNum+1 <= endBlock {
			p.prefetchBlock(blockNum + 1)
		}
		if blockNum+2 <= endBlock {
			p.prefetchBlock(blockNum + 2)
		}
	}

	// Drain channel and assert: every enqueued block appears at most
	// once, and the order is monotonically increasing.
	seen := make(map[uint64]int)
	var sequence []uint64
drain:
	for {
		select {
		case b := <-p.blockCh:
			seen[b]++
			sequence = append(sequence, b)
		default:
			break drain
		}
	}
	for b, n := range seen {
		if n > 1 {
			t.Fatalf("block %d enqueued %d times — dedup broken; full sequence: %v", b, n, sequence)
		}
	}
	for i := 1; i < len(sequence); i++ {
		if sequence[i] <= sequence[i-1] {
			t.Fatalf("non-monotonic enqueue order at index %d: %v", i, sequence)
		}
	}

	// Spot-check the expected blocks. With endBlock=1010, prefetch
	// covers 1001..1010 (cancellation + bounds). Channel capacity is 3
	// so under fast executor (no drain) we can only hold at most 3.
	// The test executor drains nothing, so the queue contains the
	// LATEST 3 enqueues. Guarantee:
	//   - sequence is sorted
	//   - no duplicates
	//   - all values in [startBlock+1, endBlock]
	for _, b := range sequence {
		if b < startBlock+1 || b > endBlock {
			t.Fatalf("enqueued block %d outside [%d, %d]", b, startBlock+1, endBlock)
		}
	}
}

// TestPrefetcher_LookaheadEnqueue_FirstIterPrime checks that the very
// first executor iteration enqueues both N+1 and N+2, while every
// subsequent iteration only enqueues N+2. This is the property that
// makes the dedup work — without the "first iter primes N+1" branch,
// the very first block would never have its N+1 prefetched.
func TestPrefetcher_LookaheadEnqueue_FirstIterPrime(t *testing.T) {
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	buf := state.NewPlainStateBuffer()
	var current atomic.Uint64
	p := newPrefetcher(context.Background(), nil, asRoDB{db}, buf, nil, nil, nil, nil, &current, false)

	// Drain in lock-step with each iteration so the channel never
	// fills (decouples the test from capacity 3).
	const startBlock = uint64(500)
	const endBlock = uint64(503)

	gotEnqueues := make(map[uint64]bool)
	drain := func() {
		for {
			select {
			case b := <-p.blockCh:
				if gotEnqueues[b] {
					t.Fatalf("duplicate enqueue of %d", b)
				}
				gotEnqueues[b] = true
			default:
				return
			}
		}
	}

	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		current.Store(blockNum)
		if blockNum == startBlock && blockNum+1 <= endBlock {
			p.prefetchBlock(blockNum + 1)
		}
		if blockNum+2 <= endBlock {
			p.prefetchBlock(blockNum + 2)
		}
		drain()
	}

	// Expected enqueues: 501 (first-iter prime), 502, 503. Block 500
	// is the start, no one prefetches it. Block 504 is past endBlock.
	want := map[uint64]bool{501: true, 502: true, 503: true}
	if len(gotEnqueues) != len(want) {
		t.Fatalf("want %d enqueues, got %d: %v", len(want), len(gotEnqueues), gotEnqueues)
	}
	for b := range want {
		if !gotEnqueues[b] {
			t.Fatalf("missing enqueue for block %d (got %v)", b, gotEnqueues)
		}
	}
}
