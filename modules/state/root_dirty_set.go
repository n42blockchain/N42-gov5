// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// The root computation's dirty set on the import path (docs/QS_MULTI_MILLION_DESIGN.md P4b).
//
// A Block-STM import leaves the block's credited recipients -- ~23k accounts
// at 163k transfers, each only ever credited -- as pending balance
// increases. The root walk used to materialise every one of them
// (getStateObject: a prefetch map read, an object, a map insert, a journal
// entry), serially, ~24 ms on the import's critical path, and the lag
// pipeline then walked the whole dirty set a second time to snapshot the
// post-state (~10 ms). With detached credits the root reads the accounts
// the processor already prefetched, computes base+credit across goroutines
// and leaves the objects to CommitBlock, which materialises pending credits
// itself, in the same order, off the root. The post-state snapshot is built
// from the root's own maps in the same walk.

// detachMinPending is the pending-credit count below which the root walk
// materialises credits serially as before.
const detachMinPending = 2048

// lastRootCredits is the most recent root walk's detached credits (high 32
// bits) and materialised ones (low 32 bits), for the import phases line.
var lastRootCredits atomic.Int64

// LastRootCredits returns how many pending credits the most recent root
// walk through a root computer took detached (P4b) and how many it
// materialised as state objects.
func LastRootCredits() (detached, materialised int) {
	v := lastRootCredits.Load()
	return int(v >> 32), int(v & 0xffffffff)
}

// SetRootPostStateHook installs fn to run once inside the next root
// computation through a RootComputer, at the point SetBeforeRootHook's hook
// runs, with the block's post-state built from the dirty set the root is
// about to apply: the same accounts and storage maps (shared, never
// modified afterwards), the wiped addresses and the code the block
// deployed. fn may read the state; it must not modify it.
func (s *IntraBlockState) SetRootPostStateHook(fn func(*PostState)) {
	s.rootPostHook = fn
}

// SetDetachedPendingCredits lets the root computation take a pending
// balance credit of an account this state never loaded straight from the
// prefetch layer (base + credit, computed in parallel) instead of
// materialising its object. The root and the dirty set it is computed from
// are unchanged; the objects are materialised by CommitBlock. Only a state
// whose reader is its prefetch layer (no recorder above it, whose read
// order would change) and that keeps no witness snapshot uses it.
func (s *IntraBlockState) SetDetachedPendingCredits(on bool) {
	s.detachPending = on
}

// pendingCredits are the detached credits of one root computation, sorted
// by address, with their post-block accounts.
type pendingCredits struct {
	addrs []types.Address
	accts []*account.StateAccount
	bis   []*BalanceIncrease // detached set while the root walk merges the dirty set
}

// release clears the detached marks once the dirty set is collected.
func (d pendingCredits) release() {
	for _, bi := range d.bis {
		bi.detached = false
	}
}

// detachPendingCredits splits pending (sorted) into the credits the root
// can take without materialising -- not loaded, not known absent, not in
// the finalized dirty set, prefetched -- with their accounts (marked
// detached until the walk has merged the journal's dirties), and the rest, which
// the caller materialises as before. Both keep pending's order.
func (s *IntraBlockState) detachPendingCredits(pending []types.Address) (pendingCredits, []types.Address) {
	if !s.detachPending || len(pending) < detachMinPending || s.accountPrefetch == nil ||
		(s.accountPrefetch.accts == nil && s.accountPrefetch.known == nil) || s.stateReader != StateReader(s.accountPrefetch) ||
		(s.snap != nil && s.snap.CanWrite()) {
		return pendingCredits{}, pending
	}
	out := make([]*account.StateAccount, len(pending))
	seeded := s.accountPrefetch
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if w := len(pending) / 1024; w < workers {
		workers = w
	}
	if workers < 1 {
		workers = 1
	}
	// Read-only over the state's maps: nothing else touches this state
	// while its root is computed.
	per := (len(pending) + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < len(pending); lo += per {
		hi := min(lo+per, len(pending))
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				out[i] = s.detachedCredit(pending[i], seeded)
			}
		}(lo, hi)
	}
	wg.Wait()
	var d pendingCredits
	rest := pending[:0:0]
	for i, addr := range pending {
		if out[i] == nil {
			rest = append(rest, addr)
			continue
		}
		bi := s.balanceInc[addr]
		bi.detached = true
		d.addrs = append(d.addrs, addr)
		d.accts = append(d.accts, out[i])
		d.bis = append(d.bis, bi)
	}
	return d, rest
}

// detachedCredit is the account getStateObject would leave for addr after
// folding its pending credit (newObject's normalisation, then the credit;
// createObject's fresh account for an absent one), or nil when addr must be
// materialised instead.
func (s *IntraBlockState) detachedCredit(addr types.Address, seeded *AccountPrefetch) *account.StateAccount {
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
	a := new(account.StateAccount)
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
	return a
}

// accountIsEmpty is stateObject.empty on an account value.
func accountIsEmpty(a *account.StateAccount) bool {
	return a.Nonce == 0 && a.Balance.IsZero() && bytes.Equal(a.CodeHash[:], emptyCodeHash)
}

// noteAlive records what a post-state needs beyond the root's maps for a
// dirty object that ends the block alive: an empty one reads as absent and
// its storage as zero (wiped), so does the old storage of a wiped one, and
// deployed code is served from the snapshot.
func (ps *PostState) noteAlive(addr types.Address, obj *stateObject, s *IntraBlockState) {
	if obj.empty() {
		ps.wiped[addr] = struct{}{}
		return
	}
	if _, w := s.storageWipes[addr]; w {
		ps.wiped[addr] = struct{}{}
	}
	if obj.code != nil && obj.dirtyCode {
		ps.code[obj.data.CodeHash] = obj.code
	}
}
