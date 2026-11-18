// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// rootRulesOn switches both root rules (UnchangedAccountNoOpTime,
// StorageNetChangeTime) on, as ApplyUnchangedAccountRule does past both
// activation times.
func rootRulesOn(ibs *IntraBlockState) {
	ibs.SetSkipUnchangedAccounts(true)
	ibs.SetStorageNetChange(true)
}

// The root rules hold on every root walk: the parallel dirty walk
// (N42_FINALIZE_FAST, with and without N42_PREHOOK_CUT) and the detached
// pending credits hand the root computer exactly the dirty set the single
// serial loop over materialised objects does -- the walk the rules were
// written for -- over random blocks with zero credits, storage set and
// reset to its starting value, wipes and recreations. The reference runs
// over the same reader with the detach switched off at the root. (These
// blocks finalize their credits before the root; TestRootRulesHoldWithHoist
// covers credits the root takes detached.)
func TestRootRulesHoldOnEveryWalk(t *testing.T) {
	prevMin := finalizeFastMin
	defer func() { finalizeFastMin = prevMin }()
	type variant struct {
		fast     bool
		min, cut int
	}
	variants := []variant{
		{min: prevMin},
		{fast: true, min: 1},
		{fast: true, min: 1, cut: 1},
		{fast: true, min: 1, cut: 2},
		{fast: true, min: prevMin},
	}
	sizes := [][2]int{{60, 200}, {400, 2000}, {9000, 30000}}
	var skippedAccts, skippedSlots int
	for _, sz := range sizes {
		for seed := int64(1); seed <= 4; seed++ {
			for _, detach := range []bool{false, true} {
				run := func(rules, materialise, fast bool, min, cut int) *ffBlock {
					finalizeFastMin = min
					defer func() { finalizeFastMin = prevMin }()
					defer SetFinalizeFastForTest(fast)()
					defer SetPrehookCutForTest(cut)()
					ffBeforeRoot = func(ibs *IntraBlockState) {
						if rules {
							rootRulesOn(ibs)
						}
						if materialise {
							ibs.SetDetachedPendingCredits(false)
						}
					}
					defer func() { ffBeforeRoot = nil }()
					ffInitialised = true
					defer func() { ffInitialised = false }()
					return runFFBlock(t, seed, sz[0], sz[1], detach)
				}
				plain := run(false, true, false, prevMin, 0)
				want := run(true, true, false, prevMin, 0)
				skippedAccts += len(plain.accts) - len(want.accts)
				skippedSlots += countSlots(plain.stor) - countSlots(want.stor)
				for _, v := range variants {
					tag := fmt.Sprintf("accts=%d seed=%d detach=%v %+v", sz[0], seed, detach, v)
					got := run(true, false, v.fast, v.min, v.cut)
					if len(want.accts) != len(got.accts) {
						t.Fatalf("%s: root accounts %d, want %d", tag, len(got.accts), len(want.accts))
					}
					for a, w := range want.accts {
						h, ok := got.accts[a]
						if !ok || (w == nil) != (h == nil) || (w != nil && !w.Equals(h)) {
							t.Fatalf("%s: root account %x: %+v, want %+v", tag, a, h, w)
						}
					}
					requireSameStorage(t, tag+" root storage", want.stor, got.stor)
					if fmt.Sprint(want.rows) != fmt.Sprint(got.rows) {
						t.Fatalf("%s: CommitBlock rows differ", tag)
					}
				}
			}
		}
	}
	if skippedAccts <= 0 || skippedSlots <= 0 {
		t.Fatalf("the rules left out %d accounts and %d slots: the blocks do not exercise them", skippedAccts, skippedSlots)
	}
}

// The hoisted pending/detach preparation (N42_FINALIZE_HOIST_DETACH) and the
// no-journal fold, under the root rules, hand the root computer the dirty set
// of the plain journaled fold with every credit materialised: a zero credit
// of an account that exists is left out of the root by the dirty-object walk
// alone, so the detach must not take it.
func TestRootRulesHoldWithHoist(t *testing.T) {
	defer SetFinalizeFastForTest(true)()
	f := newBulkFixture(20000)
	// Half of the fixture's credits credit zero (its own credits are all
	// non-zero): an existing account credited zero ends the block unchanged.
	zero := 0
	for i := range f.entries {
		if e := &f.entries[i]; e.Delta != nil && i%2 == 0 {
			e.Delta = new(uint256.Int)
			zero++
		}
	}
	if zero == 0 {
		t.Fatal("no credit set to zero")
	}
	for _, a := range f.store {
		a.Initialised = true // as a stored account decodes
	}
	run := func(rules, nj, detach, hoist bool) (njOutcome, bool, int) {
		defer SetFinalizeHoistForTest(hoist)()
		var hoisted bool
		var detached int
		njHooks.beforeFold = func(ibs *IntraBlockState) {
			if rules {
				rootRulesOn(ibs)
			}
			ibs.PrepareDetachHoist()
		}
		njHooks.afterFold = func(ibs *IntraBlockState) { ibs.ArmDetachHoist() }
		njHooks.afterRoot = func(ibs *IntraBlockState) {
			hoisted = ibs.LastRootPrepPhases().Hoisted
			detached, _ = LastRootCredits()
		}
		defer func() {
			njHooks.beforeFold, njHooks.afterFold, njHooks.afterRoot = nil, nil, nil
		}()
		return runNJBlock(t, f, nj, detach, 16), hoisted, detached
	}
	plain, _, _ := run(false, false, false, false)
	want, _, _ := run(true, false, false, false)
	if len(want.rootRows) >= len(plain.rootRows) {
		t.Fatalf("the rules left out no account (%d rows, %d without them): the fixture does not exercise them", len(want.rootRows), len(plain.rootRows))
	}
	for _, v := range []struct{ nj, detach, hoist bool }{
		{false, true, false}, {true, true, false}, {true, true, true},
	} {
		tag := fmt.Sprintf("%+v", v)
		got, hoisted, detached := run(true, v.nj, v.detach, v.hoist)
		if v.hoist && !hoisted {
			t.Fatalf("%s: the root did not take the hoisted preparation", tag)
		}
		if detached == 0 {
			t.Fatalf("%s: the root took no credit detached", tag)
		}
		if fmt.Sprint(want.rootRows) != fmt.Sprint(got.rootRows) {
			t.Fatalf("%s: root dirty set differs (%d rows, want %d)", tag, len(got.rootRows), len(want.rootRows))
		}
		if fmt.Sprint(want.rows) != fmt.Sprint(got.rows) {
			t.Fatalf("%s: CommitBlock rows differ", tag)
		}
	}
}

func countSlots(m map[types.Address]map[types.Hash]*uint256.Int) int {
	n := 0
	for _, s := range m {
		n += len(s)
	}
	return n
}
