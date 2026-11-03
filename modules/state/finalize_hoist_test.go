// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// hoistRun is one runNJBlock with the hoist hooks installed.
type hoistRun struct {
	out       njOutcome
	hoisted   bool
	late      int
	detached  int
	fallbacks uint64
}

// untrackedCredit is the address the fallback case inserts into balanceInc
// behind the late list's back.
var untrackedCredit = types.Address{0xab, 0xcd, 0xef}

func runHoistBlock(t *testing.T, f *bulkFixture, hoist, untracked bool, fold int) hoistRun {
	t.Helper()
	defer SetFinalizeHoistForTest(hoist)()
	var r hoistRun
	_, fb0 := FinalizeHoistStats()
	njHooks.beforeFold = func(ibs *IntraBlockState) { ibs.PrepareDetachHoist() }
	njHooks.afterFold = func(ibs *IntraBlockState) { ibs.ArmDetachHoist() }
	njHooks.beforeRoot = func(ibs *IntraBlockState) {
		if ibs.hoist != nil {
			r.late = len(ibs.hoist.late)
		}
		if untracked {
			// A credit no tracked path inserted: the hoist must notice it
			// is outside its universe and take the serial path.
			bi := &BalanceIncrease{count: 1}
			bi.increase.SetUint64(9)
			ibs.balanceInc[untrackedCredit] = bi
		}
	}
	njHooks.afterRoot = func(ibs *IntraBlockState) {
		r.hoisted = ibs.LastRootPrepPhases().Hoisted
		r.detached, _ = LastRootCredits()
	}
	defer func() {
		njHooks.beforeFold, njHooks.afterFold, njHooks.beforeRoot, njHooks.afterRoot = nil, nil, nil, nil
	}()
	r.out = runNJBlock(t, f, true, true, fold)
	_, fb1 := FinalizeHoistStats()
	r.fallbacks = fb1 - fb0
	return r
}

// The same block finalized with and without the hoist leaves the same
// finalize digests, root dirty set (the root computer's input), post-states
// and committed writer rows. The block end adds late credits (a fresh
// address, an already credited one, credits loaded, one created over) that
// the hoist takes from its late list; an untracked insert makes it fall
// back to the serial path with the same result.
func TestFinalizeHoistMatchesSerial(t *testing.T) {
	defer SetFinalizeFastForTest(true)()
	fixtures := map[string]*bulkFixture{
		"bulk":      newBulkFixture(20000),
		"long-runs": newRandomShardFixture(7, 20000, 0.0002),
		"no-serial": newRandomShardFixture(8, 20000, 0),
	}
	for name, f := range fixtures {
		for _, once := range []bool{false, true} {
			for _, fold := range []int{4, 16} {
				for _, untracked := range []bool{false, true} {
					tag := fmt.Sprintf("%s once=%v fold=%d untracked=%v", name, once, fold, untracked)
					func() {
						defer SetSyscallFinalizeOnceForTest(once)()
						if once {
							defer SetSyscallFinalizeParMinForTest(1)()
						}
						want := runHoistBlock(t, f, false, untracked, fold)
						got := runHoistBlock(t, f, true, untracked, fold)
						compareNJ(t, tag, want.out, got.out)
						if want.hoisted {
							t.Fatalf("%s: hoisted with the flag off", tag)
						}
						if untracked {
							if got.hoisted || got.fallbacks != 1 {
								t.Fatalf("%s: untracked credit: hoisted=%v fallbacks=%d, want the serial path", tag, got.hoisted, got.fallbacks)
							}
							return
						}
						if !got.hoisted || got.fallbacks != 0 {
							t.Fatalf("%s: hoisted=%v fallbacks=%d, want the hoisted path", tag, got.hoisted, got.fallbacks)
						}
						if got.detached == 0 || got.detached != want.detached {
							t.Fatalf("%s: detached %d credits, the serial path %d", tag, got.detached, want.detached)
						}
						if got.late == 0 {
							t.Fatalf("%s: the block end's late credits were not listed", tag)
						}
					}()
				}
			}
		}
	}
}

// A prefetch layer reseeded after the hoist armed invalidates its lookups:
// the root takes the serial path.
func TestFinalizeHoistReseedFallsBack(t *testing.T) {
	defer SetFinalizeFastForTest(true)()
	f := newBulkFixture(20000)
	run := func(hoist bool) hoistRun {
		defer SetFinalizeHoistForTest(hoist)()
		var r hoistRun
		_, fb0 := FinalizeHoistStats()
		njHooks.beforeFold = func(ibs *IntraBlockState) { ibs.PrepareDetachHoist() }
		njHooks.afterFold = func(ibs *IntraBlockState) {
			ibs.ArmDetachHoist()
			ibs.accountPrefetch.Seed(f.seed)
		}
		njHooks.afterRoot = func(ibs *IntraBlockState) { r.hoisted = ibs.LastRootPrepPhases().Hoisted }
		defer func() { njHooks.beforeFold, njHooks.afterFold, njHooks.afterRoot = nil, nil, nil }()
		r.out = runNJBlock(t, f, true, true, 16)
		_, fb1 := FinalizeHoistStats()
		r.fallbacks = fb1 - fb0
		return r
	}
	want, got := run(false), run(true)
	compareNJ(t, "reseed", want.out, got.out)
	if got.hoisted || got.fallbacks != 1 {
		t.Fatalf("hoisted=%v fallbacks=%d, want the serial path", got.hoisted, got.fallbacks)
	}
}

// With the flag off nothing is prepared or armed.
func TestFinalizeHoistOffIsInert(t *testing.T) {
	defer SetFinalizeFastForTest(true)()
	defer SetFinalizeHoistForTest(false)()
	f := newBulkFixture(20000)
	ibs, _, _ := f.build()
	ibs.SetDetachedPendingCredits(true)
	ibs.PrepareDetachHoist()
	ibs.SetFoldNoJournal(true)
	ibs.ApplyAccountsSharded(f.entries, 16)
	ibs.SetFoldNoJournal(false)
	ibs.ArmDetachHoist()
	ibs.AddBalance(types.Address{0x01, 0x02}, uint256.NewInt(1))
	if ibs.hoist != nil {
		t.Fatal("hoist prepared with the flag off")
	}
}

// BenchmarkFinalizeHoist times the root's pending list and detach on a
// block of n fold credits: the serial steps against the hoisted pass (the
// hoisted goroutine is waited for outside the timer, as the fold and the
// block end overlap it on the import).
func BenchmarkFinalizeHoist(b *testing.B) {
	defer SetFinalizeFastForTest(true)()
	defer SetFinalizeHoistForTest(true)()
	for _, n := range []int{23000, 60000, 160000} {
		known := bulkKnown{}
		entries := make([]AccountApply, n)
		for i := range entries {
			a := testAddr("hoist-bench", i)
			acc := account.NewAccount()
			acc.Balance.SetUint64(uint64(1000 + i))
			known[a] = &acc
			entries[i] = AccountApply{Addr: a, Delta: uint256.NewInt(uint64(1 + i%7))}
		}
		build := func() *IntraBlockState {
			pf := NewAccountPrefetch(mapStateReader{accts: map[types.Address]*account.StateAccount{}})
			pf.SetKnown(known)
			ibs := New(pf)
			ibs.SetAccountPrefetch(pf)
			ibs.SetDetachedPendingCredits(true)
			ibs.Reserve(n, n)
			ibs.PrepareDetachHoist()
			ibs.SetFoldNoJournal(true)
			ibs.ApplyAccountsSharded(entries, 16)
			ibs.SetFoldNoJournal(false)
			ibs.ArmDetachHoist()
			ibs.AddBalance(types.Address{0xc0, 0xff, 0xee}, uint256.NewInt(2)) // a late credit
			<-ibs.hoist.done
			return ibs
		}
		for _, hoisted := range []bool{false, true} {
			b.Run(fmt.Sprintf("n=%d/hoisted=%v", n, hoisted), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					ibs := build()
					b.StartTimer()
					if hoisted {
						d, _, ok := ibs.takeDetachHoist(false)
						if !ok {
							b.Fatal("hoist not taken")
						}
						d.release()
					} else {
						ibs.hoist = nil
						d, _ := ibs.detachPendingCredits(ibs.pendingIncreaseAddrs())
						d.release()
					}
				}
			})
		}
	}
}
