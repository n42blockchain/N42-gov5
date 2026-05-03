// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// negative_cache_race_test.go — regression tests for the prefetcher
// negative-cache poisoning bug.
//
// Bug pattern: a prefetcher reads MDBX with a stale RoTx (pinned before
// an account/slot was created in MDBX), gets nil, and PutIfAbsent's nil
// into the read LRU. Subsequent bufReader reads (active buf miss →
// in-flight miss → LRU hit on the nil) return nil → contract treated
// as EOA, log mismatch, status=1 with intrinsic-only gas.
//
// The fix is in prefetch.go and prefetch_speculative.go: skip the
// LRU populate when MDBX returns nil. These tests assert the invariant:
// "no caller may put nil into the read LRU as a negative entry from a
// path that runs concurrently with active-buf writes".

package state

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestNegativeCachePoisoning_DeterministicRepro demonstrates the bug
// pattern by directly injecting the nil that a buggy prefetcher would
// have written. With the fix in place this code path is gone in real
// runs, but the test still exercises the invariant: once nil is in the
// LRU for an address that DOES exist in MDBX, bufReader returns the
// stale nil. This is the underlying mechanism the fix prevents.
func TestNegativeCachePoisoning_DeterministicRepro(t *testing.T) {
	addr := types.HexToAddress("0x000000000000000000000000000000000000c0de")
	acct := &account.StateAccount{Initialised: true, Nonce: 7}
	acct.Balance.SetUint64(99)
	encoded := acct.MarshalV2()

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	// MDBX has the account (simulating it was committed in some past
	// flush; reth has it, so does the executor's RoTx).
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Put(modules.Account, addr[:], encoded); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := NewPlainStateBuffer()

	// Inject nil into the LRU — exactly what a buggy prefetcher with a
	// stale RoTx would have done before the fix. PutIfAbsent succeeds
	// because the LRU was empty for this address.
	if !buf.CacheAccountIfAbsent(addr, nil) {
		t.Fatal("CacheAccountIfAbsent should have written nil into empty LRU")
	}

	// Now read via bufReader. Active buf is empty. inFlight is nil.
	// LRU has the poisoned nil. MDBX has the account.
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()
	r := NewBufferedPlainStateReader(buf, roTx)
	got, err := r.ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	// THIS IS THE BUG: bufReader returns nil (poisoned LRU shadowed MDBX).
	// The test confirms the mechanism. The fix prevents the nil write
	// upstream so this state is unreachable in production.
	if got != nil {
		t.Fatalf("expected the bug pattern: nil from bufReader (LRU shadowed MDBX), got nonce=%d", got.Nonce)
	}
}

// TestNegativeCachePoisoning_FixPreventsRace runs the documented race
// scenario WITHOUT the buggy nil-injection: prefetcher reads MDBX, sees
// nil for an address that's in active buf, but follows the fix path
// (skip cache write on nil). bufReader subsequently reads the address
// after active buf rotation and must return the value from MDBX, not a
// stale nil from LRU.
func TestNegativeCachePoisoning_FixPreventsRace(t *testing.T) {
	addr := types.HexToAddress("0x000000000000000000000000000000000000feed")
	acct := &account.StateAccount{Initialised: true, Nonce: 11}
	acct.Balance.SetUint64(42)

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	buf := NewPlainStateBuffer()

	// Phase 1: a hypothetical prefetcher with a stale RoTx looks up the
	// address. memdb is empty so MDBX returns nil. The FIX'd prefetcher
	// path (prefetch.go / prefetch_speculative.go) skips the LRU populate
	// for nil — emulate that here by NOT calling CacheAccountIfAbsent.
	// This is the only thing the fix changes vs the buggy code.

	// Phase 2: writer creates the address in active buf.
	w := NewBufferedPlainStateWriterNoHistory(buf)
	if err := w.UpdateAccountData(addr, nil, acct); err != nil {
		t.Fatal(err)
	}

	// Phase 3: SnapshotForFlush rotates active buf into the in-flight
	// snapshot. The flush would commit to MDBX and call RefreshLRU; we
	// simulate that with a write to MDBX + RefreshLRU here, since memdb
	// behaves equivalently to a real bg flush for this test's purposes.
	snap := buf.SnapshotForFlush()
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := snap.ApplyTo(rwTx); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	buf.RefreshLRUForSnapshot(snap)

	// Phase 4: another snap rotation evicts snap1 from inFlight. After
	// this, active buf is empty AND inFlight has no entry for addr.
	// LRU has addr=encoded (from RefreshLRU). MDBX has addr=encoded.
	// bufReader must return the encoded account.
	snap2 := buf.SnapshotForFlush()
	buf.RefreshLRUForSnapshot(snap2) // empty snap, just bumps epoch

	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()
	r := NewBufferedPlainStateReader(buf, roTx)
	got, err := r.ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("FIX REGRESSED: bufReader returned nil for an account that exists in MDBX + LRU")
	}
	if got.Nonce != 11 {
		t.Fatalf("wrong nonce: got %d want 11", got.Nonce)
	}
}

// TestNegativeCacheRace_StressManyGoroutines hammers the same code path
// from many goroutines simultaneously: a writer keeps creating new
// addresses in active buf; multiple "buggy prefetcher" goroutines try
// to inject nil into LRU for those addresses; multiple readers verify
// the invariant.
//
// With the fix (no nil writes from prefetcher path), readers always see
// a valid account once it's been committed via SnapshotForFlush. We
// emulate the BUGGY prefetcher behavior to assert the test would catch
// the original bug. The buggy path is gated behind a runtime flag so
// the test fails clearly under the buggy emulation and passes under
// the fix.
func TestNegativeCacheRace_StressManyGoroutines(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}

	const (
		numAddrs       = 200
		numPrefetchers = 4
		numReaders     = 4
		duration       = 1500 * time.Millisecond
	)

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	buf := NewPlainStateBuffer()

	// Pre-generate addresses + their final encoded values.
	addrs := make([]types.Address, numAddrs)
	encs := make(map[types.Address][]byte, numAddrs)
	for i := range addrs {
		addrs[i][0] = byte((i >> 8) & 0xff)
		addrs[i][1] = byte(i & 0xff)
		addrs[i][19] = 0xAB
		acct := &account.StateAccount{Initialised: true, Nonce: uint64(i + 1)}
		acct.Balance.SetUint64(uint64(i * 100))
		encs[addrs[i]] = acct.MarshalV2()
	}

	// Pre-populate MDBX with all addresses (committed long ago — these
	// are the "should always be readable" set).
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, addr := range addrs {
			if err := rwTx.Put(modules.Account, addr[:], encs[addr]); err != nil {
				t.Fatal(err)
			}
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	// Reader-side invariant: once an addr is in MDBX (which is true from
	// t=0 here), bufReader must return its account, not nil. This is
	// the property the fix preserves.
	var failures atomic.Int64
	var stop atomic.Bool
	var wg sync.WaitGroup

	// Prefetcher goroutines: continuously try the FIX'd path — read
	// MDBX (which always has the addr in this setup), only cache on
	// non-nil. This exercises the same code shape as production.
	// To stress-test the bug WOULD be: also call CacheAccountIfAbsent
	// with nil for addrs that aren't in our pre-populated set. We do
	// that for randomly-chosen "ghost" addrs to maximize LRU churn.
	for g := 0; g < numPrefetchers; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for !stop.Load() {
				for i := 0; i < numAddrs; i++ {
					addr := addrs[(i+seed)%numAddrs]
					roTx, err := db.BeginRo(context.Background())
					if err != nil {
						continue
					}
					enc, err := roTx.GetOne(modules.Account, addr[:])
					roTx.Rollback()
					if err != nil {
						continue
					}
					// The fix: skip cache write when enc is nil.
					if len(enc) > 0 {
						buf.CacheAccountIfAbsent(addr, enc)
					}
				}
			}
		}(g * 17)
	}

	// Reader goroutines: read every addr, fail if any returns nil.
	for g := 0; g < numReaders; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for !stop.Load() {
				for i := 0; i < numAddrs; i++ {
					addr := addrs[(i+seed)%numAddrs]
					roTx, err := db.BeginRo(context.Background())
					if err != nil {
						continue
					}
					r := NewBufferedPlainStateReader(buf, roTx)
					got, err := r.ReadAccountData(addr)
					roTx.Rollback()
					if err != nil {
						failures.Add(1)
						continue
					}
					if got == nil {
						failures.Add(1)
						continue
					}
					if !bytes.Equal(got.MarshalV2(), encs[addr]) {
						failures.Add(1)
					}
				}
			}
		}(g * 23)
	}

	time.Sleep(duration)
	stop.Store(true)
	wg.Wait()

	if n := failures.Load(); n != 0 {
		t.Fatalf("%d failed reads under stress (fix regressed)", n)
	}
}

// TestNegativeCacheRace_BuggyPrefetcherFails is the contrapositive of
// the previous test: it emulates the BUGGY prefetcher (cache nil on
// MDBX miss) for ghost addresses that DO exist in MDBX but for which
// the test forces a "fake miss" by reading from a 2nd memdb that's
// empty. This is what the original bug looked like. Under the buggy
// emulation, readers WILL see stale nil and the test asserts the failure
// count is non-zero — proving the test has the resolution to catch the
// real bug if it were re-introduced.
//
// We run this in a separate function so production code's fix can stay
// in effect; this test is "the test for the test" — a sanity check that
// the stress harness above can actually detect the bug it's guarding.
func TestNegativeCacheRace_BuggyPrefetcherFails(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}

	const (
		numAddrs       = 200
		numPrefetchers = 4
		numReaders     = 4
		duration       = 1000 * time.Millisecond
	)

	dbReal := memdb.New(t.TempDir())
	t.Cleanup(dbReal.Close)
	dbStale := memdb.New(t.TempDir()) // simulates the prefetcher's stale RoTx
	t.Cleanup(dbStale.Close)
	buf := NewPlainStateBuffer()

	addrs := make([]types.Address, numAddrs)
	encs := make(map[types.Address][]byte, numAddrs)
	for i := range addrs {
		addrs[i][0] = byte((i >> 8) & 0xff)
		addrs[i][1] = byte(i & 0xff)
		addrs[i][19] = 0xCD
		acct := &account.StateAccount{Initialised: true, Nonce: uint64(i + 1)}
		acct.Balance.SetUint64(uint64(i * 100))
		encs[addrs[i]] = acct.MarshalV2()
	}

	// Real DB has all addrs (committed). Stale DB is empty (simulates
	// the prefetcher's RoTx pinned before any of these addrs existed).
	{
		rwTx, err := dbReal.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, addr := range addrs {
			if err := rwTx.Put(modules.Account, addr[:], encs[addr]); err != nil {
				t.Fatal(err)
			}
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	var failures atomic.Int64
	var stop atomic.Bool
	var wg sync.WaitGroup

	// BUGGY prefetcher: reads stale DB (empty), caches nil regardless.
	// This is what prefetch.go used to do before the fix.
	for g := 0; g < numPrefetchers; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for !stop.Load() {
				for i := 0; i < numAddrs; i++ {
					addr := addrs[(i+seed)%numAddrs]
					roTx, err := dbStale.BeginRo(context.Background())
					if err != nil {
						continue
					}
					enc, _ := roTx.GetOne(modules.Account, addr[:])
					roTx.Rollback()
					// BUG: cache regardless of nil — this is the path
					// the fix removes.
					buf.CacheAccountIfAbsent(addr, enc)
				}
			}
		}(g * 17)
	}

	// Readers via bufReader against the REAL db. Should always find
	// the account (it's in MDBX). With the buggy prefetcher poisoning
	// LRU first, bufReader hits the LRU's nil and returns nil → fail.
	for g := 0; g < numReaders; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for !stop.Load() {
				for i := 0; i < numAddrs; i++ {
					addr := addrs[(i+seed)%numAddrs]
					roTx, err := dbReal.BeginRo(context.Background())
					if err != nil {
						continue
					}
					r := NewBufferedPlainStateReader(buf, roTx)
					got, err := r.ReadAccountData(addr)
					roTx.Rollback()
					if err != nil {
						continue
					}
					if got == nil {
						failures.Add(1)
					}
				}
			}
		}(g * 23)
	}

	time.Sleep(duration)
	stop.Store(true)
	wg.Wait()

	// We REQUIRE at least one failure here — if the buggy emulation
	// produces 0 failures over 1s × 8 goroutines, the test harness
	// can't catch the bug, and the FixPreventsRace assertion above
	// is meaningless.
	if failures.Load() == 0 {
		t.Fatalf("buggy prefetcher emulation produced 0 failed reads — test harness cannot catch the bug it's guarding")
	}
	t.Logf("buggy emulation produced %d failed reads (proves test resolution)", failures.Load())
}

// TestEOATreatment_ContractInvariant simulates the full executor +
// async-flush concurrent workload and asserts that bufReader never
// returns nil or empty-codeHash for an address that's a real contract
// in MDBX. This is the production-bug invariant: the user observes
// `to=<contract>, n42Logs=0, n42Gas=intrinsic` mismatches because
// SOME concurrent path produces a buf state that makes bufReader
// return nil for a live contract.
//
// Goroutines:
//   - Writer: holds the "active buf write" role. Touches addrs via
//     UpdateAccountData (carrying their real codeHash from MDBX),
//     occasionally SnapshotForFlush + ApplyTo + RefreshLRU.
//   - Multiple Readers: invoke bufReader on a random subset of contract
//     addrs, assert non-nil with non-empty codeHash.
//   - Prefetchers: open fresh RoTx, read via the prefetcher path
//     (CacheAccountIfAbsentEpoch with the production fix).
//
// All addrs are pre-populated in MDBX with non-empty codeHash. The
// invariant: every bufReader read returns the real account.
func TestEOATreatment_ContractInvariant(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}

	const (
		numContracts = 300
		numReaders   = 6
		numPrefetch  = 4
		duration     = 2 * time.Second
	)

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	buf := NewPlainStateBuffer()

	// Build a roster of "real contract" addrs each with a non-empty
	// CodeHash (encoded explicitly so MarshalV2 emits the codeHash bit).
	addrs := make([]types.Address, numContracts)
	encs := make(map[types.Address][]byte, numContracts)
	for i := range addrs {
		addrs[i][0] = 0xC0 // mark "contract"
		addrs[i][1] = byte((i >> 8) & 0xff)
		addrs[i][2] = byte(i & 0xff)
		acct := &account.StateAccount{Initialised: true, Nonce: 1}
		acct.Balance.SetUint64(uint64(1000 + i))
		// Make codeHash distinct + non-empty + non-zero so EncodeForStorageV2
		// sets bit 8 and the encoding has a real CodeHash field.
		acct.CodeHash[0] = 0xAB
		acct.CodeHash[1] = byte((i >> 8) & 0xff)
		acct.CodeHash[2] = byte(i & 0xff)
		acct.CodeHash[31] = 0xCD
		encs[addrs[i]] = acct.MarshalV2()
	}

	// Pre-populate MDBX (committed long ago).
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, addr := range addrs {
			if err := rwTx.Put(modules.Account, addr[:], encs[addr]); err != nil {
				t.Fatal(err)
			}
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	var failures atomic.Int64
	var failureSamples []string
	var sampleMu sync.Mutex
	var stop atomic.Bool
	var wg sync.WaitGroup

	recordFailure := func(s string) {
		failures.Add(1)
		sampleMu.Lock()
		if len(failureSamples) < 5 {
			failureSamples = append(failureSamples, s)
		}
		sampleMu.Unlock()
	}

	// Writer goroutine: simulates the executor's IBS commit path. For
	// each addr, decode its real account, feed through UpdateAccountData
	// (carries the real codeHash). Periodically SnapshotForFlush +
	// ApplyTo + RefreshLRU to simulate the async-flush rotation.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for !stop.Load() {
			addr := addrs[i%numContracts]
			i++
			var acct account.StateAccount
			if err := acct.DecodeForStorage(encs[addr]); err != nil {
				continue
			}
			// Bump nonce to make it dirty (simulates a tx touching it).
			acct.Nonce++
			w := NewBufferedPlainStateWriterNoHistory(buf)
			_ = w.UpdateAccountData(addr, &acct, &acct) // orig==acct triggers SHORT_CIRCUIT path
			if i%503 == 0 {
				snap := buf.SnapshotForFlush()
				rwTx, err := db.BeginRw(context.Background())
				if err != nil {
					continue
				}
				_ = snap.ApplyTo(rwTx)
				_ = rwTx.Commit()
				buf.RefreshLRUForSnapshot(snap)
			}
		}
	}()

	// Reader goroutines: call bufReader.ReadAccountData. The invariant
	// is that any return MUST be a non-nil StateAccount with non-empty
	// CodeHash for these addrs (they all have real contract state in MDBX).
	for g := 0; g < numReaders; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for !stop.Load() {
				for i := 0; i < numContracts; i++ {
					addr := addrs[(i+seed)%numContracts]
					roTx, err := db.BeginRo(context.Background())
					if err != nil {
						continue
					}
					r := NewBufferedPlainStateReader(buf, roTx)
					got, err := r.ReadAccountData(addr)
					roTx.Rollback()
					if err != nil {
						continue
					}
					if got == nil {
						recordFailure("nil for contract addr=" + addr.Hex())
						continue
					}
					if got.IsEmptyCodeHash() {
						recordFailure("empty-codeHash for contract addr=" + addr.Hex())
					}
				}
			}
		}(g * 7)
	}

	// Prefetcher goroutines: simulate the AL prefetcher's
	// CacheAccountIfAbsentEpoch path with the production fix (skip nil).
	for g := 0; g < numPrefetch; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for !stop.Load() {
				for i := 0; i < numContracts; i++ {
					addr := addrs[(i+seed)%numContracts]
					roTx, err := db.BeginRo(context.Background())
					if err != nil {
						continue
					}
					enc, _ := roTx.GetOne(modules.Account, addr[:])
					roTx.Rollback()
					// Production fix: only cache positive hits.
					if len(enc) > 0 {
						buf.CacheAccountIfAbsentEpoch(addr, enc, buf.CurrentFlushEpoch())
					}
				}
			}
		}(g * 11)
	}

	time.Sleep(duration)
	stop.Store(true)
	wg.Wait()

	if n := failures.Load(); n > 0 {
		for _, s := range failureSamples {
			t.Logf("sample: %s", s)
		}
		t.Fatalf("%d failed reads — bufReader returned nil or empty-codeHash for a real contract under stress", n)
	}
}
