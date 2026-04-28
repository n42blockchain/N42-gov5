// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// TestPrefetchStateReader_LookupOrder verifies the reader walks tiers
// in the documented order: InFlight snapshot → LRU → MDBX. The reader
// MUST NOT populate the LRU on MDBX hit (prevents staleness race with
// the executor's RefreshLRUForSnapshot).
func TestPrefetchStateReader_LookupOrder(t *testing.T) {
	addr := types.HexToAddress("0x000000000000000000000000000000000000beef")

	mdbxAcct := &account.StateAccount{Initialised: true, Nonce: 1}
	mdbxAcct.Balance.SetUint64(100)
	lruAcct := &account.StateAccount{Initialised: true, Nonce: 2}
	lruAcct.Balance.SetUint64(200)

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	require := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	{
		rwTx, err := db.BeginRw(context.Background())
		require(t, err)
		require(t, rwTx.Put(modules.Account, addr.Bytes(), mdbxAcct.MarshalV2()))
		require(t, rwTx.Commit())
	}

	buf := state.NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	require(t, err)
	defer roTx.Rollback()
	r := newPrefetchStateReader(buf, roTx)

	// Tier 3: MDBX hit → returns mdbxAcct, but does NOT publish into
	// the LRU. Account caching from the prefetcher path was removed
	// after a deterministic "insufficient funds" failure at block
	// 2520771: the prefetcher's RoTx can lag the latest commit, so a
	// PutIfAbsentEpoch can land a stale balance into an LRU entry that
	// was just evicted by S3-FIFO. The fix is to never write account
	// state from the prefetcher; bufReader's own MDBX-miss path
	// (executor's per-interval stable RoTx) fills the LRU correctly
	// on demand.
	got, err := r.ReadAccountData(addr)
	require(t, err)
	if got == nil || got.Nonce != 1 {
		t.Fatalf("MDBX hit: got nonce=%v want 1", got)
	}
	if _, present := buf.LookupReadAccount(addr); present {
		t.Fatalf("LRU MUST NOT be populated by the prefetcher account read — see prefetch_speculative.go ReadAccountData rationale")
	}

	// Externally Put lruAcct into the LRU (simulating RefreshLRU after
	// a flush). The next prefetch read MUST observe the LRU value
	// (consulted via LookupReadAccount in the prefetchStateReader),
	// not re-query MDBX.
	buf.CacheAccount(addr, lruAcct)
	got, err = r.ReadAccountData(addr)
	require(t, err)
	if got == nil || got.Nonce != 2 {
		t.Fatalf("LRU hit: got nonce=%v want 2", got)
	}
}

// TestPrefetchStateReader_NegativeMDBX verifies the reader returns nil
// for an absent address WITHOUT populating the LRU with a negative
// entry. Caching nil here is unsafe: our pinned RoTx may pre-date a
// flush that just created the address (active buf has it, MDBX doesn't);
// the nil would then outlive the active buf via PutIfAbsent and produce
// a stale "EOA" read in the next interval (status=1 with intrinsic-only
// gas, log mismatch). MDBX miss for a truly-absent account is a one-time
// cost; the silent-corruption risk dwarfs the cache benefit.
func TestPrefetchStateReader_NegativeMDBX(t *testing.T) {
	addr := types.HexToAddress("0x000000000000000000000000000000000000dead")

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	buf := state.NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()
	r := newPrefetchStateReader(buf, roTx)

	got, err := r.ReadAccountData(addr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for absent address, got %+v", got)
	}
	// LRU MUST NOT be populated with a negative-cache entry — see func doc.
	if _, present := buf.LookupReadAccount(addr); present {
		t.Fatalf("LRU should NOT hold a negative-cache entry: stale-RoTx race risk")
	}
}

// TestPrefetchStateReader_StorageRoundtrip verifies storage reads return
// the correct MDBX value and DO NOT populate the LRU (race-safety).
func TestPrefetchStateReader_StorageRoundtrip(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee01")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	val := []byte{0x42}

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot[:])
		if err := rwTx.Put(modules.Storage, key, val); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()
	r := newPrefetchStateReader(buf, roTx)

	got, err := r.ReadAccountStorage(addr, &slot)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 0x42 {
		t.Fatalf("MDBX hit: got %x", got)
	}
	// LRU MUST NOT be populated by the prefetcher — same reasoning as
	// the account read in TestPrefetchStateReader_LookupOrder. The
	// prefetcher's RoTx can lag the latest commit, so any
	// PutIfAbsentEpoch from this path can land a stale value into an
	// evicted LRU slot. bufReader.ReadAccountStorage's own MDBX-miss
	// fill (using the executor's per-interval stable RoTx) is the
	// only authorized cache write site for storage in this commit.
	if _, present := buf.LookupReadStorage(addr, slot); present {
		t.Fatalf("LRU MUST NOT be populated by the prefetcher storage read")
	}
}

// TestPrefetchStateReader_RaceAvoidance simulates the real concurrency
// pattern: one prefetcher goroutine (with its own RoTx) reading via the
// LRU + MDBX tiers, while multiple "executor-side" goroutines mutate the
// LRU via CacheAccount. MDBX RoTx is NOT goroutine-safe, so each
// goroutine must own its own; the prefetcher pattern in the production
// runSpeculative call already does that. Run under `go test -race`.
func TestPrefetchStateReader_RaceAvoidance(t *testing.T) {
	if testing.Short() {
		t.Skip("race test skipped in short mode")
	}
	addr := types.HexToAddress("0x000000000000000000000000000000000000face")

	acct := &account.StateAccount{Initialised: true, Nonce: 1}
	acct.Balance.SetUint64(1)

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Put(modules.Account, addr.Bytes(), acct.MarshalV2()); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()

	const writers = 4
	const iters = 500

	var wg sync.WaitGroup

	// Single prefetcher goroutine with its own RoTx.
	wg.Add(1)
	go func() {
		defer wg.Done()
		roTx, err := db.BeginRo(context.Background())
		if err != nil {
			return
		}
		defer roTx.Rollback()
		r := newPrefetchStateReader(buf, roTx)
		for j := 0; j < iters; j++ {
			_, _ = r.ReadAccountData(addr)
		}
	}()

	// Multiple executor-side goroutines hammering CacheAccount. In the
	// real executor only one goroutine writes the LRU at a time, but
	// S3-FIFO is mutex-protected so concurrent writers exercise the
	// same locking that protects single-writer + concurrent-prefetcher.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				a := &account.StateAccount{Initialised: true, Nonce: uint64(j)}
				a.Balance.SetUint64(uint64(j))
				buf.CacheAccount(addr, a)
			}
		}()
	}

	wg.Wait()
}

// TestPrefetcher_SpeculativeFlagDisabled verifies the prefetcher falls
// back to the static AccessList path when SpeculativeEnabled=false. We
// don't actually run a block — just construct the prefetcher and
// confirm SpeculativeEnabled is wired through.
func TestPrefetcher_SpeculativeFlagDisabled(t *testing.T) {
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	buf := state.NewPlainStateBuffer()
	var current atomic.Uint64

	p := newPrefetcher(context.Background(), nil, asRoDB{db}, buf, nil, nil, nil, nil, &current, false)
	if p.speculativeEnabled {
		t.Fatalf("expected speculativeEnabled=false")
	}
	if p.engine != nil {
		t.Fatalf("expected engine=nil when not provided")
	}
}

// TestCancelled verifies the cancellation predicate returns true when
// the executor has caught up to the prefetched block.
func TestCancelled(t *testing.T) {
	var current atomic.Uint64
	p := &prefetcher{currentBlockNum: &current}

	// Executor at block 100: prefetcher working on block 101 NOT cancelled.
	current.Store(100)
	if p.cancelled(101) {
		t.Fatalf("not cancelled: executor at 100, prefetch 101")
	}
	// Executor reaches 101: prefetcher of block 101 IS cancelled.
	current.Store(101)
	if !p.cancelled(101) {
		t.Fatalf("expected cancelled: executor at 101 == prefetch 101")
	}
	// Executor past 101.
	current.Store(102)
	if !p.cancelled(101) {
		t.Fatalf("expected cancelled: executor at 102 > prefetch 101")
	}
	// Nil currentBlockNum: never cancelled.
	p2 := &prefetcher{currentBlockNum: nil}
	if p2.cancelled(101) {
		t.Fatalf("nil currentBlockNum: must not be cancelled")
	}
}

// asRoDB lets us pass an *memdb.MemoryMutationKv as kv.RoDB to
// newPrefetcher in the disabled-flag test.
type asRoDB struct{ kv.RwDB }
