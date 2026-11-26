package internal

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func receiptsOfLen(n int) block.Receipts {
	r := make(block.Receipts, n)
	for i := range r {
		r[i] = &block.Receipt{}
	}
	return r
}

// TestReceiptsCacheBudget: the cache is bounded by the receipts it holds,
// evicts oldest first, and never takes a block larger than half the budget.
func TestReceiptsCacheBudget(t *testing.T) {
	c := newReceiptsCache(16, 100)
	h := func(b byte) types.Hash { return types.Hash{b} }

	c.add(h(1), receiptsOfLen(40))
	c.add(h(2), receiptsOfLen(40))
	if _, ok := c.get(h(1)); !ok {
		t.Fatal("block 1 should be cached")
	}
	c.add(h(3), receiptsOfLen(40)) // 120 > 100: evicts the oldest (block 2, since 1 was just read)
	if _, ok := c.get(h(2)); ok {
		t.Fatal("block 2 should have been evicted")
	}
	if c.total != 80 {
		t.Fatalf("total = %d, want 80", c.total)
	}
	c.add(h(4), receiptsOfLen(51)) // over budget/2: not cached
	if _, ok := c.get(h(4)); ok {
		t.Fatal("oversized block must not be cached")
	}
	c.add(h(1), receiptsOfLen(40)) // duplicate add does not double-count
	if c.total != 80 {
		t.Fatalf("total after duplicate add = %d, want 80", c.total)
	}
	c.purge()
	if c.total != 0 {
		t.Fatalf("total after purge = %d", c.total)
	}
	var nilCache *receiptsCache
	nilCache.add(h(5), receiptsOfLen(1))
	if _, ok := nilCache.get(h(5)); ok {
		t.Fatal("nil cache must miss")
	}
}

// TestReceiptsCacheLoadSharesConcurrentMisses: concurrent loads of one block
// run the loader once and all get its result.
func TestReceiptsCacheLoadSharesConcurrentMisses(t *testing.T) {
	c := newReceiptsCache(4, 100)
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	got := make([]block.Receipts, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], _ = c.load(types.Hash{9}, func() (block.Receipts, error) {
				calls.Add(1)
				<-release
				return receiptsOfLen(3), nil
			})
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("loader ran %d times, want 1", n)
	}
	for i, r := range got {
		if len(r) != 3 {
			t.Fatalf("caller %d got %d receipts", i, len(r))
		}
	}
}
