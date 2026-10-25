// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// N42_FINALIZE_FAST=1: the root walk's dirty-object loop
// (computeRootViaComputer, the part of a block's Finalize before the lag
// pipeline's post-state hook, on the import's critical path) in three
// passes instead of one:
//
//  1. serial, in address order: getStateObject for every dirty address --
//     the only step that can read through the state reader (the witness
//     recorder observes that order) or change the state (a pending credit
//     folds, an absent credited account is created). The order and the
//     calls are the original loop's.
//  2. parallel over chunks of the sorted slice: each object's post-block
//     account copy, its storage ops and its post-state notes, read-only
//     over the state and the objects.
//  3. serial: the entries put into the root's maps (the detached
//     credits go in while pass 2 runs).
//
// The detached credits' split (detachPendingCreditsFast) and the journal
// merge (one balanceInc lookup for a detached credit) are cheaper too.
//
// The maps the root computer receives, and the post-state, are the same
// as the single loop's. Only the plain commitment path takes it (the LtHash
// path, which also collects originals, keeps the single loop), and only
// from finalizeFastMin dirty objects. Default off: the single loop.
var finalizeFastOn = parseFinalizeFast(os.Getenv("N42_FINALIZE_FAST"))

func parseFinalizeFast(v string) bool {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return true
	}
	return false
}

// FinalizeFastOn reports N42_FINALIZE_FAST.
func FinalizeFastOn() bool { return finalizeFastOn }

// SetFinalizeFastForTest switches N42_FINALIZE_FAST and returns the
// restore.
func SetFinalizeFastForTest(on bool) func() {
	prev := finalizeFastOn
	finalizeFastOn = on
	return func() { finalizeFastOn = prev }
}

// finalizeFastMin is the dirty-object count from which the parallel walk is
// taken (below it the goroutines cost more than they save).
var finalizeFastMin = 2048

// SetFinalizeFastMinForTest sets finalizeFastMin and returns the restore.
func SetFinalizeFastMinForTest(n int) func() {
	prev := finalizeFastMin
	finalizeFastMin = n
	return func() { finalizeFastMin = prev }
}

// finalizeFastWalks counts the root walks that took the parallel loop.
var finalizeFastWalks atomic.Uint64

// FinalizeFastWalks returns how many root walks took the parallel loop
// (since start).
func FinalizeFastWalks() uint64 { return finalizeFastWalks.Load() }

// finalizeFastChunk is the smallest chunk of dirty objects one worker
// takes.
const finalizeFastChunk = 1024

// dirtyEntry is one dirty object's contribution to the root's maps.
type dirtyEntry struct {
	acct  *account.StateAccount // nil: deleted
	slots map[types.Hash]*uint256.Int
	wiped bool   // post-state: reads as absent / its old storage as zero
	code  []byte // post-state: deployed code (non-nil: record it)
}

// collectDirtyParallel is the dirty-object loop of computeRootViaComputer
// without originals (see finalizeFastOn): addrs is the sorted dirty set.
//
// The detached credits (the single loop's tail, d) are put into accounts
// while the workers run: no detached address is in the dirty set (the
// detach skips every address in it or loaded, and the merge leaves the
// detached ones out), so the order of the two kinds of insert is free.
func (s *IntraBlockState) collectDirtyParallel(addrs []types.Address, accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int, post *PostState, d *pendingCredits) {
	finalizeFastWalks.Add(1)
	objs := make([]*stateObject, len(addrs))
	for i, addr := range addrs {
		objs[i] = s.getStateObject(addr)
	}
	out := make([]dirtyEntry, len(addrs))
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if w := len(addrs) / finalizeFastChunk; w < workers {
		workers = w
	}
	if workers < 1 {
		workers = 1
	}
	per := (len(addrs) + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < len(addrs); lo += per {
		hi := min(lo+per, len(addrs))
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			// The alive accounts of this chunk in one allocation: every
			// one stays referenced by the root's map as long as the slab.
			slab := make([]account.StateAccount, hi-lo)
			for i := lo; i < hi; i++ {
				s.dirtyEntryOf(addrs[i], objs[i], &slab[i-lo], &out[i])
			}
		}(lo, hi)
	}
	for i, addr := range d.addrs {
		a := d.accts[i]
		accounts[addr] = a
		if post != nil && accountIsEmpty(a) {
			post.wiped[addr] = struct{}{}
		}
	}
	wg.Wait()
	for i, addr := range addrs {
		e := &out[i]
		accounts[addr] = e.acct
		if e.slots != nil {
			storage[addr] = e.slots
		}
		if post != nil {
			if e.wiped {
				post.wiped[addr] = struct{}{}
			}
			if e.code != nil {
				post.code[e.acct.CodeHash] = e.code
			}
		}
	}
}

// dirtyEntryOf fills e for one dirty object exactly as the single loop
// does for it (acct is the slot for its account copy). Read-only over the
// state and obj.
func (s *IntraBlockState) dirtyEntryOf(addr types.Address, obj *stateObject, acct *account.StateAccount, e *dirtyEntry) {
	if obj == nil || obj.deleted || obj.selfdestructed {
		e.wiped = true
		if wiped := s.activeWipedSlots(addr); len(wiped) > 0 {
			slots := make(map[types.Hash]*uint256.Int, len(wiped))
			for key := range wiped {
				slots[key] = new(uint256.Int)
			}
			if obj != nil {
				for _, key := range obj.dirtyKeys {
					if _, ok := slots[key]; !ok {
						slots[key] = new(uint256.Int)
					}
				}
			}
			e.slots = slots
		} else if obj != nil && obj.blockOriginLen() > 0 {
			slots := make(map[types.Hash]*uint256.Int, obj.blockOriginLen())
			obj.eachBlockOrigin(func(key types.Hash, _ uint256.Int) {
				slots[key] = new(uint256.Int)
			})
			e.slots = slots
		}
		return
	}
	acct.Copy(&obj.data)
	e.acct = acct
	if obj.empty() {
		e.wiped = true
	} else {
		if _, w := s.storageWipes[addr]; w {
			e.wiped = true
		}
		if obj.code != nil && obj.dirtyCode {
			e.code = obj.code
		}
	}
	wiped := s.activeWipedSlots(addr)
	if len(wiped) == 0 && len(obj.dirtyKeys) == 0 {
		return
	}
	slots := make(map[types.Hash]*uint256.Int, len(wiped)+len(obj.dirtyKeys))
	for key := range wiped {
		slots[key] = new(uint256.Int)
	}
	for _, key := range obj.dirtyKeys {
		v := obj.dirtyValue(key)
		slots[key] = new(uint256.Int).Set(&v)
	}
	if len(slots) == 0 {
		return
	}
	e.slots = slots
}

// detachPendingCreditsFast is detachPendingCredits' split under
// N42_FINALIZE_FAST, from the same gate: each worker takes one chunk of
// pending, builds its credits' accounts in one allocation, marks their
// balance increases detached (one per address: no two workers share one)
// and keeps its chunk's detached and remaining addresses in pending's
// order; the chunks are then concatenated. Same result as the serial
// split, without its second balanceInc lookup and appends per credit.
func (s *IntraBlockState) detachPendingCreditsFast(pending []types.Address, seeded *AccountPrefetch, workers int) (pendingCredits, []types.Address) {
	per := (len(pending) + workers - 1) / workers
	type chunk struct {
		d    pendingCredits
		rest []types.Address
	}
	chunks := make([]chunk, (len(pending)+per-1)/per)
	var wg sync.WaitGroup
	for c := range chunks {
		lo := c * per
		hi := min(lo+per, len(pending))
		wg.Add(1)
		go func(c, lo, hi int) {
			defer wg.Done()
			ch := &chunks[c]
			slab := make([]account.StateAccount, hi-lo)
			ch.d.addrs = make([]types.Address, 0, hi-lo)
			ch.d.accts = make([]*account.StateAccount, 0, hi-lo)
			ch.d.bis = make([]*BalanceIncrease, 0, hi-lo)
			for i := lo; i < hi; i++ {
				a := &slab[i-lo]
				bi := s.detachedCreditInto(pending[i], seeded, a)
				if bi == nil {
					ch.rest = append(ch.rest, pending[i])
					continue
				}
				bi.detached = true
				ch.d.addrs = append(ch.d.addrs, pending[i])
				ch.d.accts = append(ch.d.accts, a)
				ch.d.bis = append(ch.d.bis, bi)
			}
		}(c, lo, hi)
	}
	wg.Wait()
	var nd, nr int
	for c := range chunks {
		nd += len(chunks[c].d.addrs)
		nr += len(chunks[c].rest)
	}
	var d pendingCredits
	if nd > 0 {
		d.addrs = make([]types.Address, 0, nd)
		d.accts = make([]*account.StateAccount, 0, nd)
		d.bis = make([]*BalanceIncrease, 0, nd)
	}
	rest := make([]types.Address, 0, nr)
	for c := range chunks {
		ch := &chunks[c]
		d.addrs = append(d.addrs, ch.d.addrs...)
		d.accts = append(d.accts, ch.d.accts...)
		d.bis = append(d.bis, ch.d.bis...)
		rest = append(rest, ch.rest...)
	}
	return d, rest
}

// detachedCreditInto is detachedCredit building the account in a and
// returning the credit's balance increase (nil: addr must be materialised;
// a is then unused).
func (s *IntraBlockState) detachedCreditInto(addr types.Address, seeded *AccountPrefetch, a *account.StateAccount) *BalanceIncrease {
	if s.lastObj != nil && s.lastAddr == addr {
		return nil
	}
	if _, ok := s.stateObjects[addr]; ok {
		return nil
	}
	if _, ok := s.nilAccounts[addr]; ok {
		return nil
	}
	if _, ok := s.stateObjectsDirty[addr]; ok {
		return nil
	}
	bi, ok := s.balanceInc[addr]
	if !ok || bi == nil || bi.transferred {
		return nil
	}
	base, ok := seeded.lookupSeeded(addr)
	if !ok {
		return nil
	}
	if base != nil {
		a.Copy(base)
	}
	if !a.Initialised {
		a.Balance.SetUint64(0)
		a.Initialised = true
	}
	if a.CodeHash == (types.Hash{}) {
		a.CodeHash = emptyCodeHashH
	}
	a.Balance.Add(&a.Balance, &bi.increase)
	return bi
}
