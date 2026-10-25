// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// N42_SYSCALL_FINALIZE_ONCE=1: the Prague block end's FinalizeTx work, two
// levers behind one flag.
//
//  1. ProcessPragueSystemCalls (package internal) runs the EIP-7002 and the
//     EIP-7251 system calls back to back and finalizes once, when the state
//     takes its pending credits detached (DetachedPendingCredits): the
//     first FinalizeTx walked the parallel fold's whole dirty set (~31k
//     addresses on the fleet) and the second the ~23k pending credits that
//     the first one left in the journal.
//  2. FinalizeTx's no-op-writer walk over a dirty set of at least
//     syscallFinalizeParMin addresses runs in parallel (finalizeNoopParallel).
//
// Default off: per-call finalize and the serial walk.
var syscallFinalizeOnceOn = parseFinalizeFast(os.Getenv("N42_SYSCALL_FINALIZE_ONCE"))

// SyscallFinalizeOnceOn reports N42_SYSCALL_FINALIZE_ONCE.
func SyscallFinalizeOnceOn() bool { return syscallFinalizeOnceOn }

// SetSyscallFinalizeOnceForTest switches N42_SYSCALL_FINALIZE_ONCE and
// returns the restore.
func SetSyscallFinalizeOnceForTest(on bool) func() {
	prev := syscallFinalizeOnceOn
	syscallFinalizeOnceOn = on
	return func() { syscallFinalizeOnceOn = prev }
}

// syscallFinalizeParMin is the dirty-set size from which the no-op walk
// runs in parallel.
var syscallFinalizeParMin = 2048

// SetSyscallFinalizeParMinForTest sets syscallFinalizeParMin and returns
// the restore.
func SetSyscallFinalizeParMinForTest(n int) func() {
	prev := syscallFinalizeParMin
	syscallFinalizeParMin = n
	return func() { syscallFinalizeParMin = prev }
}

// syscallFinalizeParWalks counts the FinalizeTx walks that ran in parallel.
var syscallFinalizeParWalks atomic.Uint64

// SyscallFinalizeParWalks returns how many no-op FinalizeTx walks ran in
// parallel (since start).
func SyscallFinalizeParWalks() uint64 { return syscallFinalizeParWalks.Load() }

// DetachedPendingCredits reports SetDetachedPendingCredits.
func (s *IntraBlockState) DetachedPendingCredits() bool { return s.detachPending }

// noopFinalizeChunk is the smallest chunk of addresses one worker takes.
const noopFinalizeChunk = 1024

// Per-address outcome of the parallel no-op walk.
const (
	noopKeep  = iota // no object, a live pending credit: stays in the journal
	noopDrop         // no object, no live credit: leaves the journal
	noopDirty        // an object: finalized, marked dirty, leaves the journal
)

// finalizeNoopParallel is FinalizeTx's no-op-writer loop (finalizeFlags and
// the stateObjectsDirty insert for every object in the journal's dirty
// set, the drop list under keepPending) fused with clearCurrentTxFlags,
// whose per-object step depends only on that object's deleted flag, which
// only finalizeFlags sets. The workers touch only their own objects and
// read the maps; the map writes are serial. The dropped addresses are
// returned for FinalizeTx to delete after the journal reset.
//
// Equivalence with the serial path: promoteWipes, which runs between the
// serial loop and clearCurrentTxFlags, reads only storageWipes, so the
// fused order is the same per object.
func (sdb *IntraBlockState) finalizeNoopParallel(policy accountWritePolicy, keepPending bool) []types.Address {
	syscallFinalizeParWalks.Add(1)
	dirties := sdb.journal.dirties
	addrs := make([]types.Address, 0, len(dirties))
	for addr := range dirties {
		addrs = append(addrs, addr)
	}
	out := make([]byte, len(addrs))
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if w := len(addrs) / noopFinalizeChunk; w < workers {
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
			for i := lo; i < hi; i++ {
				addr := addrs[i]
				so, exist := sdb.stateObjects[addr]
				if !exist {
					if bi := sdb.balanceInc[addr]; bi == nil || bi.transferred {
						out[i] = noopDrop
					} else {
						out[i] = noopKeep
					}
					continue
				}
				finalizeFlags(policy, addr, so)
				if !so.deleted {
					so.created = false
					so.selfdestructed = false
				}
				out[i] = noopDirty
			}
		}(lo, hi)
	}
	wg.Wait()
	var drop []types.Address
	if keepPending {
		drop = make([]types.Address, 0, len(addrs))
	}
	for i, addr := range addrs {
		switch out[i] {
		case noopDirty:
			sdb.stateObjectsDirty[addr] = struct{}{}
			if keepPending {
				drop = append(drop, addr)
			}
		case noopDrop:
			if keepPending {
				drop = append(drop, addr)
			}
		}
	}
	return drop
}

// FinalizeSharedCaller is the part of a FinalizeTx between two block-end
// system calls that the second call can observe, for the one account both
// calls touch: their caller (the system address). If addr is dirty in the
// journal and finalizing it would delete it (EIP-161 empty, or
// self-destructed), it is finalized now as the FinalizeTx would: deleted,
// marked dirty and taken out of the journal's dirty set. The second call
// then finds it deleted and replaces it (a reset, which dirties nothing),
// exactly as after a full FinalizeTx, and the closing FinalizeTx leaves the
// replacement alone. An address that would stay alive is left to the
// closing FinalizeTx, which gives it the same flags. Only for the no-op
// writer on a state that keeps its pending credits detached (the
// FinalizeTx it stands in for drops every loaded address from the journal).
func (s *IntraBlockState) FinalizeSharedCaller(chainRules *params.Rules, addr types.Address) {
	if _, dirty := s.journal.dirties[addr]; !dirty {
		return
	}
	so, ok := s.stateObjects[addr]
	if !ok {
		return
	}
	policy := newAccountWritePolicy(chainRules)
	if !so.selfdestructed && !policy.shouldRemoveEmptyAccount(addr, so) {
		return
	}
	finalizeFlags(policy, addr, so)
	s.stateObjectsDirty[addr] = struct{}{}
	delete(s.journal.dirties, addr)
}

// FinalizeDigestForTest describes what a FinalizeTx leaves behind, for
// differential tests: every live object in address order with its
// lifecycle flags, balance, nonce and stateObjectsDirty membership, then
// the addresses still dirty in the journal.
func (s *IntraBlockState) FinalizeDigestForTest() string {
	var b strings.Builder
	for _, addr := range sortedAddresses(s.stateObjects) {
		so := s.stateObjects[addr]
		_, dirty := s.stateObjectsDirty[addr]
		fmt.Fprintf(&b, "%s del=%v cre=%v sd=%v dirty=%v bal=%s nonce=%d\n",
			addr.Hex(), so.deleted, so.created, so.selfdestructed, dirty, so.data.Balance.String(), so.data.Nonce)
	}
	for _, addr := range sortedAddresses(s.stateObjectsDirty) {
		if _, ok := s.stateObjects[addr]; !ok {
			fmt.Fprintf(&b, "dirty-only %s\n", addr.Hex())
		}
	}
	for _, addr := range sortedAddresses(s.journal.dirties) {
		fmt.Fprintf(&b, "journal %s\n", addr.Hex())
	}
	return b.String()
}
