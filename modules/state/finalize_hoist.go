// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// N42_FINALIZE_HOIST_DETACH=1: the root walk's pending-credit list and the
// detach's prefetch reads (computeRootViaComputer's first two steps, on the
// import's critical path before the lag pipeline's post-state hook) are
// prepared off that path, from the moment the no-journal fold has listed
// its credits.
//
// PrepareDetachHoist (before the fold) records the few balance increases
// already pending; ArmDetachHoist (after it) copies the fold's credit list
// and starts a goroutine that sorts it and looks every address up in the
// prefetch layer (the seeded set, then the known base cache), keeping each
// hit's account normalised as newObject leaves it. Every AddBalance that
// inserts a balance increase after the prepare is listed (late). The
// goroutine reads only its own copy of the addresses, the seeded map (never
// written once installed) and the known cache (safe for concurrent use).
//
// The root then takes the candidate universe U = fold credits + late +
// pre-fold keys (sorted, distinct) instead of walking balanceInc, and in one
// parallel pass over U applies exactly pendingIncreaseAddrs' filter and
// detachedCreditInto's checks against the state as it stands at the root.
// It is exact because:
//
//   - the pass counts the members of U that are keys of balanceInc; the
//     result is used only when that count equals len(balanceInc), so every
//     key is in U and the filtered U is the pending set, in address order;
//     otherwise (a credit inserted by a path the late list misses) the root
//     takes the serial path for the whole block;
//   - a base lookup that hit at the hoist hits identically at the root: the
//     prefetch layer's generation is unchanged (no Seed, no SetKnown), the
//     seeded map is immutable and the known cache is insert-only with the
//     first value winning; a miss is looked up again at the root;
//   - the credit itself (the balance increase as of the root) is added at
//     the root, never at the hoist.
//
// Default off: the serial steps, unchanged.
var finalizeHoistOn = parseFinalizeFast(os.Getenv("N42_FINALIZE_HOIST_DETACH"))

// FinalizeHoistOn reports N42_FINALIZE_HOIST_DETACH.
func FinalizeHoistOn() bool { return finalizeHoistOn }

// SetFinalizeHoistForTest switches N42_FINALIZE_HOIST_DETACH and returns
// the restore.
func SetFinalizeHoistForTest(on bool) func() {
	prev := finalizeHoistOn
	finalizeHoistOn = on
	return func() { finalizeHoistOn = prev }
}

// hoistMaxPreKeys is the largest pending set before the fold the hoist
// records; a larger one leaves the block on the serial path.
const hoistMaxPreKeys = 1024

var finalizeHoistUsed, finalizeHoistFallbacks atomic.Uint64

// FinalizeHoistStats returns how many root walks used the hoisted
// pending/detach result and how many armed hoists fell back to the serial
// path (since start).
func FinalizeHoistStats() (used, fallbacks uint64) {
	return finalizeHoistUsed.Load(), finalizeHoistFallbacks.Load()
}

// detachHoist is one block's hoisted pending/detach preparation.
type detachHoist struct {
	late  []types.Address // pre-fold keys and balanceInc inserts since (main goroutine only)
	armed bool
	pfGen uint64
	done  chan struct{}
	// Written by the goroutine, read after done.
	addrs []types.Address        // the fold's credits, sorted and distinct
	base  []account.StateAccount // the normalised base account where has
	has   []bool
}

// PrepareDetachHoist starts recording the balance increases a hoist must
// consider (see finalizeHoistOn); the caller invokes it right before the
// no-journal fold. A no-op unless the flag, the fast finalize and the
// detached credits are on.
func (s *IntraBlockState) PrepareDetachHoist() {
	s.hoist = nil
	if !finalizeHoistOn || !finalizeFastOn || !s.detachPending || s.accountPrefetch == nil ||
		len(s.balanceInc) > hoistMaxPreKeys || s.trace || s.tracer != nil {
		return
	}
	h := &detachHoist{}
	for a := range s.balanceInc {
		h.late = append(h.late, a)
	}
	s.hoist = h
}

// ArmDetachHoist starts the hoisted preparation over the credits the fold
// listed; the caller invokes it right after the fold.
func (s *IntraBlockState) ArmDetachHoist() {
	h := s.hoist
	if h == nil || h.armed {
		s.hoist = nil
		return
	}
	pf := s.accountPrefetch
	if pf == nil || s.stateReader != StateReader(pf) || len(s.foldCredits) < detachMinPending {
		s.hoist = nil
		return
	}
	h.armed = true
	h.pfGen = pf.gen
	h.done = make(chan struct{})
	addrs := append([]types.Address(nil), s.foldCredits...)
	go h.run(addrs, pf.accts, pf.known)
}

// noteHoistInsert lists a balance increase inserted outside the fold's
// credit list.
func (s *IntraBlockState) noteHoistInsert(addr types.Address) {
	s.hoist.late = append(s.hoist.late, addr)
}

// run is the hoisted goroutine: sort, dedupe, base lookups.
func (h *detachHoist) run(addrs []types.Address, accts map[types.Address]*account.StateAccount, known KnownAccounts) {
	defer close(h.done)
	sortAddresses(addrs)
	addrs = dedupeSortedAddresses(addrs)
	n := len(addrs)
	h.addrs = addrs
	h.base = make([]account.StateAccount, n)
	h.has = make([]bool, n)
	workers := hoistWorkers(n)
	per := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += per {
		hi := min(lo+per, n)
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				b, ok := lookupSeededIn(accts, known, addrs[i])
				if !ok {
					continue
				}
				h.has[i] = true
				normaliseCreditBase(&h.base[i], b)
			}
		}(lo, hi)
	}
	wg.Wait()
}

func hoistWorkers(n int) int {
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if w := n / 1024; w < workers {
		workers = w
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

// lookupSeededIn is AccountPrefetch.lookupSeeded over a captured seeded
// map and known cache.
func lookupSeededIn(accts map[types.Address]*account.StateAccount, known KnownAccounts, addr types.Address) (*account.StateAccount, bool) {
	if a, ok := accts[addr]; ok {
		return a, true
	}
	if known != nil {
		return known.Peek(addr)
	}
	return nil, false
}

// normaliseCreditBase is detachedCreditInto's account before the credit:
// a (zero) takes base, normalised as newObject / createObject leave it.
func normaliseCreditBase(a, base *account.StateAccount) {
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
}

func dedupeSortedAddresses(addrs []types.Address) []types.Address {
	if len(addrs) < 2 {
		return addrs
	}
	w := 1
	for i := 1; i < len(addrs); i++ {
		if addrs[i] != addrs[w-1] {
			addrs[w] = addrs[i]
			w++
		}
	}
	return addrs[:w]
}

// takeDetachHoist is computeRootViaComputer's pending list and detach from
// the hoisted preparation: the detached credits and the remaining pending
// addresses, exactly as detachPendingCredits(pendingIncreaseAddrs()) would
// return them, and true; false when the caller must take the serial path.
func (s *IntraBlockState) takeDetachHoist(ltEnabled bool) (pendingCredits, []types.Address, bool) {
	h := s.hoist
	s.hoist = nil
	if h == nil || !h.armed {
		return pendingCredits{}, nil, false
	}
	pf := s.accountPrefetch
	if ltEnabled || !finalizeFastOn || !s.detachPending || pf == nil || pf.gen != h.pfGen ||
		(pf.accts == nil && pf.known == nil) || s.stateReader != StateReader(pf) ||
		(s.snap != nil && s.snap.CanWrite()) {
		finalizeHoistFallbacks.Add(1)
		return pendingCredits{}, nil, false
	}
	<-h.done

	// The universe: the fold's credits merged with the late inserts.
	uAddr, uSrc := h.addrs, []int32(nil)
	if len(h.late) > 0 {
		late := append([]types.Address(nil), h.late...)
		sortAddresses(late)
		late = dedupeSortedAddresses(late)
		uAddr = make([]types.Address, 0, len(h.addrs)+len(late))
		uSrc = make([]int32, 0, len(h.addrs)+len(late))
		i, j := 0, 0
		for i < len(h.addrs) || j < len(late) {
			switch {
			case j == len(late) || (i < len(h.addrs) && compareAddresses(h.addrs[i], late[j]) < 0):
				uAddr = append(uAddr, h.addrs[i])
				uSrc = append(uSrc, int32(i))
				i++
			case i == len(h.addrs) || compareAddresses(late[j], h.addrs[i]) < 0:
				uAddr = append(uAddr, late[j])
				uSrc = append(uSrc, -1)
				j++
			default: // equal
				uAddr = append(uAddr, h.addrs[i])
				uSrc = append(uSrc, int32(i))
				i++
				j++
			}
		}
	}

	n := len(uAddr)
	workers := hoistWorkers(n)
	per := (n + workers - 1) / workers
	type chunk struct {
		present, pending int
		d                pendingCredits
		rest             []types.Address
	}
	chunks := make([]chunk, (n+per-1)/per)
	var wg sync.WaitGroup
	for c := range chunks {
		lo := c * per
		hi := min(lo+per, n)
		wg.Add(1)
		go func(c, lo, hi int) {
			defer wg.Done()
			ch := &chunks[c]
			ch.d.addrs = make([]types.Address, 0, hi-lo)
			ch.d.accts = make([]*account.StateAccount, 0, hi-lo)
			ch.d.bis = make([]*BalanceIncrease, 0, hi-lo)
			var slab []account.StateAccount
			for i := lo; i < hi; i++ {
				addr := uAddr[i]
				bi, ok := s.balanceInc[addr]
				if !ok {
					continue
				}
				ch.present++
				if bi == nil || bi.transferred {
					continue
				}
				ch.pending++
				// detachedCreditInto's checks, on the state at the root.
				if s.lastObj != nil && s.lastAddr == addr {
					ch.rest = append(ch.rest, addr)
					continue
				}
				if _, ok := s.stateObjects[addr]; ok {
					ch.rest = append(ch.rest, addr)
					continue
				}
				if _, ok := s.nilAccounts[addr]; ok {
					ch.rest = append(ch.rest, addr)
					continue
				}
				if _, ok := s.stateObjectsDirty[addr]; ok {
					ch.rest = append(ch.rest, addr)
					continue
				}
				var a *account.StateAccount
				src := int32(i)
				if uSrc != nil {
					src = uSrc[i]
				}
				if src >= 0 && h.has[src] {
					a = &h.base[src]
				} else {
					base, ok := pf.lookupSeeded(addr)
					if !ok {
						ch.rest = append(ch.rest, addr)
						continue
					}
					if len(slab) == 0 {
						slab = make([]account.StateAccount, min(hi-i, 256))
					}
					a = &slab[0]
					slab = slab[1:]
					normaliseCreditBase(a, base)
				}
				a.Balance.Add(&a.Balance, &bi.increase)
				bi.detached = true
				ch.d.addrs = append(ch.d.addrs, addr)
				ch.d.accts = append(ch.d.accts, a)
				ch.d.bis = append(ch.d.bis, bi)
			}
		}(c, lo, hi)
	}
	wg.Wait()

	var present, pending, nd, nr int
	for c := range chunks {
		present += chunks[c].present
		pending += chunks[c].pending
		nd += len(chunks[c].d.addrs)
		nr += len(chunks[c].rest)
	}
	if present != len(s.balanceInc) || pending < detachMinPending {
		// A key outside the universe, or a set the serial detach would
		// not split: undo the marks, take the serial path.
		for c := range chunks {
			chunks[c].d.release()
		}
		finalizeHoistFallbacks.Add(1)
		return pendingCredits{}, nil, false
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
	finalizeHoistUsed.Add(1)
	return d, rest, true
}
