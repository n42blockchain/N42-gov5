// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"os"
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// N42_PREHOOK_CUT: shorter root preamble (computeRootViaComputer up to the
// lag pipeline's post-state hook, fPreHook). That span is on both the
// execution chain and the import chain of the lag pipeline, so every
// millisecond cut there comes off the block cadence. Only the
// N42_FINALIZE_FAST parallel dirty walk is affected (the plain commitment
// path from finalizeFastMin dirty objects, with detached credits).
//
//	1: two serial steps of the walk move off its critical path.
//	   a. The root's account map is made, and the detached credits put
//	      in (~23k inserts at 163k transfers, ~2 ms), on their own
//	      goroutine right after the journal merge, beside the dirty set's
//	      sort, the object resolution and the per-object workers, instead
//	      of only beside the workers. The goroutine makes the map itself:
//	      its core zeroes the memory it then fills (a map made on the
//	      walk's goroutine made the inserts twice as slow). The map is
//	      handed to nobody before the goroutine is joined (before the dirty entries go in, before the
//	      hook). The detached addresses are disjoint from the dirty set
//	      (the detach skips every dirty or loaded address, the merge
//	      leaves the detached ones out), so the insert order does not
//	      change the map.
//	   b. The serial getStateObject pass over the sorted dirty set
//	      (~8k objects, ~1.5 ms) first resolves, across goroutines and
//	      read-only, every address whose call would only return its live
//	      object: present in stateObjects, not the state's last-resolved
//	      address, owing no pending balance increase, no access-list
//	      tracker recording. Only the others are passed to getStateObject,
//	      serially and in address order (the only calls that can read the
//	      store, which the witness recorder observes, or change the
//	      state), each seeing the last-resolved object it would have seen
//	      in the single pass; the state's last-resolved object ends as the
//	      single pass leaves it.
//	2: 1, and the detach (hoisted lookups and checking pass, or the
//	   serial path's split) and the parallel walk use up to 16 goroutines
//	   instead of 8 (chunked in
//	   order and concatenated: the same result).
//
// Default 0: the walk as before.
var prehookCutMode = parsePrehookCut(os.Getenv("N42_PREHOOK_CUT"))

func parsePrehookCut(v string) int {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return 1
	case "2":
		return 2
	}
	return 0
}

// PrehookCutMode reports N42_PREHOOK_CUT (0 off).
func PrehookCutMode() int { return prehookCutMode }

// SetPrehookCutForTest sets N42_PREHOOK_CUT's mode and returns the
// restore.
func SetPrehookCutForTest(mode int) func() {
	prev := prehookCutMode
	prehookCutMode = mode
	return func() { prehookCutMode = prev }
}

// finalizeWorkerCap is the most goroutines the root preamble's parallel
// steps take.
func finalizeWorkerCap() int {
	if prehookCutMode >= 2 {
		return 16
	}
	return 8
}

// creditInserts is N42_PREHOOK_CUT's goroutine putting the detached credits
// into the root's account map (and an empty one into the post-state's
// wiped set), as collectDirtyParallel otherwise does itself.
type creditInserts struct {
	done  chan struct{}
	took  time.Duration
	start time.Time
	// Made by the goroutine (its core zeroes the map it fills), read after
	// done.
	accounts map[types.Address]*account.StateAccount
	post     *PostState
}

// startCreditInserts makes the root's account map (size hint n) and, when
// withPost, its post-state over it and storage, and puts d's credits in, on
// its own goroutine. Neither is touched by anyone else until wait returns.
func startCreditInserts(d *pendingCredits, n int, storage map[types.Address]map[types.Hash]*uint256.Int, withPost bool) *creditInserts {
	ci := &creditInserts{done: make(chan struct{}), start: time.Now()}
	addrs, accts := d.addrs, d.accts
	go func() {
		defer close(ci.done)
		accounts := make(map[types.Address]*account.StateAccount, n)
		var post *PostState
		if withPost {
			post = &PostState{accounts: accounts, storage: storage,
				wiped: make(map[types.Address]struct{}), code: make(map[types.Hash][]byte)}
		}
		ci.accounts, ci.post = accounts, post
		for i, addr := range addrs {
			a := accts[i]
			accounts[addr] = a
			if post != nil && accountIsEmpty(a) {
				post.wiped[addr] = struct{}{}
			}
		}
		ci.took = time.Since(ci.start)
	}()
	return ci
}

// wait joins the inserts and returns how long the caller waited.
func (ci *creditInserts) wait() time.Duration {
	t0 := time.Now()
	<-ci.done
	return time.Since(t0)
}

// resolveDirtyObjects fills objs[i] = getStateObject(addrs[i]) for the
// sorted dirty set, with exactly the single serial pass's calls, reads,
// state changes and final last-resolved object (N42_PREHOOK_CUT 1b).
func (s *IntraBlockState) resolveDirtyObjects(addrs []types.Address, objs []*stateObject, workers int) {
	if t := s.bal; (t != nil && !t.muted) || workers < 2 {
		for i, addr := range addrs {
			objs[i] = s.getStateObject(addr)
		}
		return
	}
	// Read-only pass: the calls that would only hand back a live object.
	lastAddr, hasLast := s.lastAddr, s.lastObj != nil
	per := (len(addrs) + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < len(addrs); lo += per {
		hi := min(lo+per, len(addrs))
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			checkInc := len(s.balanceInc) > 0
			for i := lo; i < hi; i++ {
				addr := addrs[i]
				if hasLast && addr == lastAddr {
					continue // the cached-object path: serial
				}
				obj := s.stateObjects[addr]
				if obj == nil {
					continue
				}
				if checkInc {
					if bi, ok := s.balanceInc[addr]; ok && (bi == nil || !bi.transferred) {
						continue // the call folds a pending increase: serial
					}
				}
				objs[i] = obj
			}
		}(lo, hi)
	}
	wg.Wait()
	// The other calls, in address order, each after the last-resolved
	// object the single pass would have left before it.
	pending := -1
	for i, addr := range addrs {
		if objs[i] != nil {
			pending = i
			continue
		}
		if pending >= 0 {
			s.lastAddr, s.lastObj = addrs[pending], objs[pending]
			pending = -1
		}
		objs[i] = s.getStateObject(addr)
	}
	if pending >= 0 {
		s.lastAddr, s.lastObj = addrs[pending], objs[pending]
	}
}
