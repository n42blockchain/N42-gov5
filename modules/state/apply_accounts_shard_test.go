// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// slabView adds what snapshotState leaves out: the unused slab, and which
// slab entry (or none) each pending credit took.
func slabView(ibs *IntraBlockState, slab []BalanceIncrease) map[string]any {
	idx := map[types.Address]int{}
	if len(slab) > 0 {
		lo := uintptr(unsafe.Pointer(&slab[0]))
		hi := lo + uintptr(len(slab))*unsafe.Sizeof(slab[0])
		for a, bi := range ibs.balanceInc {
			p := uintptr(unsafe.Pointer(bi))
			if p >= lo && p < hi {
				idx[a] = int((p - lo) / unsafe.Sizeof(slab[0]))
			} else {
				idx[a] = -1
			}
		}
	}
	return map[string]any{"slabLeft": len(ibs.biSlab), "slabIdx": idx, "lastObjSet": ibs.lastObj != nil}
}

// checkSharded runs f's entries through ApplyAccounts and through
// ApplyAccountsSharded at several worker counts and compares every map,
// the journal record by record, the slab, the state after a revert across
// the fold and the committed rows.
func checkSharded(t *testing.T, f *bulkFixture) {
	t.Helper()
	for _, workers := range []int{2, 4, 8, 16} {
		ref, refPf, refSnap := f.build()
		refSlab := ref.biSlab
		ref.ApplyAccounts(f.entries, 8)
		got, gotPf, gotSnap := f.build()
		gotSlab := got.biSlab
		got.ApplyAccountsSharded(f.entries, workers)

		compareStates(t, "after apply", f.snapshotState(ref, refPf), f.snapshotState(got, gotPf))
		compareStates(t, "slab", slabView(ref, refSlab), slabView(got, gotSlab))
		if ref.lastObj != nil && ref.lastObj.address != got.lastObj.address {
			t.Fatalf("workers %d: last object %x vs %x", workers, ref.lastObj.address, got.lastObj.address)
		}
		if w, g := f.snapshotState(ref, refPf)["journal"], f.snapshotState(got, gotPf)["journal"]; !reflect.DeepEqual(w, g) {
			t.Fatalf("workers %d: journal differs", workers)
		}

		ref.RevertToSnapshot(refSnap)
		got.RevertToSnapshot(gotSnap)
		compareStates(t, "after revert", f.snapshotState(ref, refPf), f.snapshotState(got, gotPf))

		ref2, _, _ := f.build()
		ref2.ApplyAccounts(f.entries, 8)
		got2, _, _ := f.build()
		got2.ApplyAccountsSharded(f.entries, workers)
		if w, g := committed(t, ref2), committed(t, got2); !reflect.DeepEqual(w, g) {
			t.Fatalf("workers %d: committed rows differ", workers)
		}
	}
}

// Short runs: the bulk fixture's shaped entries (live objects, base reads,
// remembered absent, pending credits, deletions) break every run below
// applyShardMinRun, so each is applied in place.
func TestApplyAccountsShardedShortRuns(t *testing.T) {
	checkSharded(t, newBulkFixture(6000))
}

// newRandomShardFixture is a block whose entries are mostly prepared full
// entries and credits of unloaded accounts (long runs, the split pass),
// with entries that need the per-entry calls scattered at rate sparse:
// base reads, deletions, credits of live objects, the zero ripemd credit.
func newRandomShardFixture(seed int64, n int, sparse float64) *bulkFixture {
	rng := rand.New(rand.NewSource(seed))
	f := &bulkFixture{
		store: map[types.Address]*account.StateAccount{},
		known: bulkKnown{},
		seed:  map[types.Address]*account.StateAccount{},
	}
	for i := 0; i < n; i++ {
		a := testAddr("shard", int(seed)*1_000_000+i)
		inStore := rng.Intn(4) != 0
		if inStore {
			acc := account.NewAccount()
			acc.Balance.SetUint64(uint64(rng.Intn(1 << 30)))
			acc.Nonce = uint64(rng.Intn(9))
			f.store[a] = &acc
		}
		serial := rng.Float64() < sparse
		switch {
		case serial && rng.Intn(2) == 0:
			// not cached: a base read
		case inStore:
			f.known[a] = f.store[a]
		default:
			f.known[a] = nil
		}
		e := AccountApply{Addr: a}
		switch r := rng.Intn(10); {
		case r < 5:
			e.Balance.SetUint64(uint64(rng.Intn(1 << 30)))
			e.Nonce = uint64(rng.Intn(9) + 1)
		case r < 9:
			e.Delta = uint256.NewInt(uint64(rng.Intn(3))) // zero credits included
		default:
			if serial {
				e.Delete = true
			} else {
				e.Balance.SetUint64(0) // written empty
			}
		}
		f.entries = append(f.entries, e)
	}
	if n > 10 {
		// The zero ripemd credit loads the account.
		f.entries[n/3] = AccountApply{Addr: ripemd, Delta: new(uint256.Int)}
	}
	// Pre-state shaping as in newBulkFixture, on full entries.
	pick := func(from int) types.Address {
		for i := from; i < len(f.entries); i++ {
			e := f.entries[i]
			if e.Delta == nil && !e.Delete {
				if _, ok := f.known[e.Addr]; ok {
					return e.Addr
				}
			}
		}
		panic("no full entry")
	}
	f.live = pick(n / 5)
	f.pending = pick(n / 4)
	f.wiped = pick(n / 2)
	f.nilAcc = types.Address{0xee, byte(seed)}
	f.known[f.nilAcc] = nil
	f.entries = append(f.entries, AccountApply{Addr: f.nilAcc, Balance: *uint256.NewInt(3), Nonce: 1})
	f.deletedLive = pick(2 * n / 3)
	return f
}

// Long runs: many random blocks, the split pass with entries needing the
// per-entry calls in between, and a block with none of them at all.
func TestApplyAccountsShardedMatchesApplyAccounts(t *testing.T) {
	sizes := []int{3000, 9000, 20000}
	rates := []float64{0, 0.0002, 0.002}
	if testing.Short() {
		sizes = sizes[:2]
	}
	seed := int64(1)
	for _, n := range sizes {
		for _, sparse := range rates {
			for k := 0; k < 3; k++ {
				checkSharded(t, newRandomShardFixture(seed, n, sparse))
				seed++
			}
		}
	}
}

// A state with a pending credit on an address the block credits again: the
// credit adds onto the existing increase instead of taking the slab.
func TestApplyAccountsShardedExistingCredit(t *testing.T) {
	f := newRandomShardFixture(99, 5000, 0)
	for _, e := range f.entries {
		if e.Delta != nil && e.Addr != ripemd {
			f.pending = e.Addr // build() credits it before the fold
			break
		}
	}
	checkSharded(t, f)
}

// benchShardShape is a qs block's account fold: n senders written in full,
// recipients only credited (all cached in the known set).
func benchShardShape(n, recipients int) (map[types.Address]*account.StateAccount, bulkKnown, []AccountApply) {
	store := map[types.Address]*account.StateAccount{}
	known := bulkKnown{}
	var entries []AccountApply
	for i := 0; i < n+recipients; i++ {
		a := testAddr("bench-shard", i)
		acc := account.NewAccount()
		acc.Balance.SetUint64(uint64(i + 1))
		store[a] = &acc
		known[a] = &acc
		if i < n {
			entries = append(entries, AccountApply{Addr: a, Balance: *uint256.NewInt(uint64(i)), Nonce: 1})
		} else {
			entries = append(entries, AccountApply{Addr: a, Delta: uint256.NewInt(3)})
		}
	}
	return store, known, entries
}

// BenchmarkApplyAccountsSplit163k reports the bulk fold's preparation and
// its serial pass, and the sharded pass at 4, 8 and 16 workers.
func BenchmarkApplyAccountsSplit163k(b *testing.B) {
	store, known, entries := benchShardShape(163_000, 23_000)
	mk := func() *IntraBlockState {
		pf := NewAccountPrefetch(mapStateReader{accts: store})
		pf.SetKnown(known)
		ibs := New(pf)
		ibs.SetAccountPrefetch(pf)
		ibs.Reserve(163_000, 23_000)
		return ibs
	}
	b.Run("prepare-only/w=4", func(b *testing.B) {
		for it := 0; it < b.N; it++ {
			b.StopTimer()
			ibs := mk()
			prep := make([]preparedAccount, len(entries))
			b.StartTimer()
			var wg sync.WaitGroup
			per := (len(entries) + 3) / 4
			for lo := 0; lo < len(entries); lo += per {
				hi := min(lo+per, len(entries))
				wg.Add(1)
				go func(lo, hi int) {
					defer wg.Done()
					ibs.prepareAccounts(ibs.accountPrefetch, entries[lo:hi], prep[lo:hi])
				}(lo, hi)
			}
			wg.Wait()
		}
	})
	b.Run("bulk/w=4", func(b *testing.B) {
		for it := 0; it < b.N; it++ {
			b.StopTimer()
			ibs := mk()
			b.StartTimer()
			ibs.ApplyAccounts(entries, 4)
		}
	})
	for _, w := range []int{4, 8, 16} {
		b.Run(fmt.Sprintf("sharded/w=%d", w), func(b *testing.B) {
			for it := 0; it < b.N; it++ {
				b.StopTimer()
				ibs := mk()
				b.StartTimer()
				ibs.ApplyAccountsSharded(entries, w)
			}
		})
	}
}
