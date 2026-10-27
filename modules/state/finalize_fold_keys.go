// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
)

// N42_FINALIZE_FOLD_KEYS=1: the root walk (computeRootViaComputer) takes
// its two sorted address sets -- the pending credits (pendingIncreaseAddrs)
// and the dirty set (sortedAddresses(stateObjectsDirty)) -- from the
// no-journal fold instead of collecting and sorting the two maps.
//
// The fold knows the bulk of both: the accounts it wrote (foldWritten, put
// into stateObjectsDirty) and the credits it inserted (foldCredits with
// their balance increases). BeginFoldKeys, right before the fold, lists the
// keys both maps already hold; from then on every other insert into them is
// logged (markDirty, AddBalance), and a delete from either breaks the keys.
// CaptureFoldKeys, right after the fold, copies the fold's lists and sorts
// them on a goroutine of their own while the block end runs.
//
// At the root, each set is the merge of the fold's sorted list with its
// sorted, deduplicated log. Every listed fold key is still in its map (no
// delete since the capture, and an insert never replaces an entry), and a
// logged key is looked up, so the merge holds as many distinct keys as the
// map exactly when it holds all of them: a count that differs falls back to
// the maps. A fold credit's increase is the map's own entry, so its
// transferred flag is read through the listed pointer -- or not at all
// when none was set at the capture and none has been set or cleared
// since (the pending set is then the fold's sorted list as it is, when
// nothing was logged beside it). Either set is the
// same slice the maps give, in the same order; the detach then skips its
// balanceInc lookup per credit.
//
// Default off.
var finalizeFoldKeysOn = parseFinalizeFast(os.Getenv("N42_FINALIZE_FOLD_KEYS"))

// FinalizeFoldKeysOn reports N42_FINALIZE_FOLD_KEYS.
func FinalizeFoldKeysOn() bool { return finalizeFoldKeysOn }

// SetFinalizeFoldKeysForTest switches N42_FINALIZE_FOLD_KEYS and returns
// the restore.
func SetFinalizeFoldKeysForTest(on bool) func() {
	prev := finalizeFoldKeysOn
	finalizeFoldKeysOn = on
	return func() { finalizeFoldKeysOn = prev }
}

// foldKeysVerify makes every use of the fold keys compare its sets with
// the maps' and panic on a difference (tests).
var foldKeysVerify bool

// SetFoldKeysVerifyForTest switches foldKeysVerify and returns the restore.
func SetFoldKeysVerifyForTest(on bool) func() {
	prev := foldKeysVerify
	foldKeysVerify = on
	return func() { foldKeysVerify = prev }
}

// foldKeysPreMax is the most keys the two maps may hold before the fold
// for BeginFoldKeys to track them (they are logged one by one).
const foldKeysPreMax = 4096

var foldKeysUsed, foldKeysFallback atomic.Uint64

// FoldKeysCounts returns how many root walks took both sets from the fold
// keys and how many fell back to the maps for at least one (since start).
func FoldKeysCounts() (used, fallback uint64) {
	return foldKeysUsed.Load(), foldKeysFallback.Load()
}

// foldKeys is one fold's handover to the root walk (see above).
type foldKeys struct {
	w0, c0 int // the fold lists' lengths at BeginFoldKeys

	captured bool
	sorted   sync.WaitGroup
	written  []types.Address // sorted after sorted.Wait
	credits  []types.Address // sorted after sorted.Wait
	bis      []*BalanceIncrease

	// live: no listed credit was transferred at the capture; flips: the
	// transferred flags set or cleared since. With both clear, every
	// listed credit is pending without reading its flag.
	live  bool
	flips int

	// dirtyLog, biLog: the keys inserted into stateObjectsDirty and
	// balanceInc other than by the fold, and those present at the begin.
	dirtyLog, biLog []types.Address
	// broken: a key was deleted from one of the maps.
	broken bool
}

// BeginFoldKeys starts tracking the maps for the fold about to run (see
// above). No-op when the flag is off or the maps already hold many keys.
func (sdb *IntraBlockState) BeginFoldKeys() {
	sdb.fk = nil
	if !finalizeFoldKeysOn || len(sdb.stateObjectsDirty)+len(sdb.balanceInc) > foldKeysPreMax {
		return
	}
	fk := &foldKeys{w0: len(sdb.foldWritten), c0: len(sdb.foldCredits)}
	for addr := range sdb.stateObjectsDirty {
		fk.dirtyLog = append(fk.dirtyLog, addr)
	}
	for addr := range sdb.balanceInc {
		fk.biLog = append(fk.biLog, addr)
	}
	sdb.fk = fk
}

// CaptureFoldKeys takes the fold's written accounts and credits, right
// after the fold, and sorts them in the background.
func (sdb *IntraBlockState) CaptureFoldKeys() {
	fk := sdb.fk
	if fk == nil || fk.captured {
		return
	}
	if len(sdb.foldWritten) < fk.w0 || len(sdb.foldCredits) < fk.c0 {
		sdb.fk = nil // the lists were reset during the fold
		return
	}
	fk.captured = true
	fk.written = slices.Clone(sdb.foldWritten[fk.w0:])
	fk.credits = slices.Clone(sdb.foldCredits[fk.c0:])
	fk.bis = slices.Clone(sdb.foldBIs[fk.c0:])
	// In fold order the increases are read in their slab's order; sorted,
	// at the root, they would be read at random.
	fk.live = true
	for _, bi := range fk.bis {
		if bi.transferred {
			fk.live = false
			break
		}
	}
	fk.sorted.Add(1)
	go func() {
		defer fk.sorted.Done()
		sortAddresses(fk.written)
		sortAddressesWith(fk.credits, fk.bis)
	}()
}

// markDirty puts addr into stateObjectsDirty, logging a new key for the
// fold keys.
func (sdb *IntraBlockState) markDirty(addr types.Address) {
	if sdb.fk == nil {
		sdb.stateObjectsDirty[addr] = struct{}{}
		return
	}
	n := len(sdb.stateObjectsDirty)
	sdb.stateObjectsDirty[addr] = struct{}{}
	if len(sdb.stateObjectsDirty) != n {
		sdb.fk.dirtyLog = append(sdb.fk.dirtyLog, addr)
	}
}

// noteBalanceIncInsert logs a new balanceInc key for the fold keys.
func (sdb *IntraBlockState) noteBalanceIncInsert(addr types.Address) {
	if sdb.fk != nil {
		sdb.fk.biLog = append(sdb.fk.biLog, addr)
	}
}

// noteTransferFlip counts a transferred flag set or cleared, for the fold
// keys.
func (sdb *IntraBlockState) noteTransferFlip() {
	if sdb.fk != nil {
		sdb.fk.flips++
	}
}

// noteFoldKeyDelete breaks the fold keys: a key left one of the maps.
func (sdb *IntraBlockState) noteFoldKeyDelete() {
	if sdb.fk != nil {
		sdb.fk.broken = true
	}
}

// foldKeysReady waits for the captured lists and reports whether they can
// be used.
func (sdb *IntraBlockState) foldKeysReady() bool {
	fk := sdb.fk
	if fk == nil || !fk.captured {
		return false
	}
	fk.sorted.Wait()
	return !fk.broken
}

// foldKeysPending is pendingIncreaseAddrs from the fold keys, with each
// credit's balance increase. ok is false when the keys do not cover
// balanceInc.
func (sdb *IntraBlockState) foldKeysPending() (addrs []types.Address, bis []*BalanceIncrease, ok bool) {
	if !sdb.foldKeysReady() {
		return nil, nil, false
	}
	fk := sdb.fk
	logged := sortedDistinct(fk.biLog)
	fk.biLog = logged
	live := fk.live && fk.flips == 0
	if live {
		// The common case: no logged key beyond the fold's credits, which
		// are all pending: the fold's sorted lists themselves.
		extra := false
		for _, a := range logged {
			if _, in := slices.BinarySearchFunc(fk.credits, a, compareAddresses); in {
				continue
			}
			if _, present := sdb.balanceInc[a]; present {
				extra = true
				break
			}
		}
		if !extra {
			if len(fk.credits) != len(sdb.balanceInc) {
				return nil, nil, false
			}
			sdb.verifyFoldKeysPending(fk.credits, fk.bis)
			return fk.credits, fk.bis, true
		}
	}
	addrs = make([]types.Address, 0, len(fk.credits)+len(logged))
	bis = make([]*BalanceIncrease, 0, len(fk.credits)+len(logged))
	keys := len(fk.credits)
	i, j := 0, 0
	for i < len(fk.credits) || j < len(logged) {
		c := 1
		switch {
		case i == len(fk.credits):
		case j == len(logged):
			c = -1
		default:
			c = compareAddresses(fk.credits[i], logged[j])
		}
		if c <= 0 {
			if bi := fk.bis[i]; live || !bi.transferred {
				addrs = append(addrs, fk.credits[i])
				bis = append(bis, bi)
			}
			i++
			if c == 0 {
				j++
			}
			continue
		}
		if bi, present := sdb.balanceInc[logged[j]]; present {
			keys++
			if bi != nil && !bi.transferred {
				addrs = append(addrs, logged[j])
				bis = append(bis, bi)
			}
		}
		j++
	}
	if keys != len(sdb.balanceInc) {
		return nil, nil, false
	}
	sdb.verifyFoldKeysPending(addrs, bis)
	return addrs, bis, true
}

// verifyFoldKeysPending panics, under foldKeysVerify, when addrs and bis
// are not the pending credits the map gives.
func (sdb *IntraBlockState) verifyFoldKeysPending(addrs []types.Address, bis []*BalanceIncrease) {
	if !foldKeysVerify {
		return
	}
	if want := sdb.pendingIncreaseAddrs(); !slices.Equal(want, addrs) {
		panic(fmt.Sprintf("fold keys: pending credits differ (%d vs %d)", len(addrs), len(want)))
	}
	for k, a := range addrs {
		if sdb.balanceInc[a] != bis[k] {
			panic(fmt.Sprintf("fold keys: credit %x is not the map's", a))
		}
	}
}

// foldKeysDirty is sortedAddresses(stateObjectsDirty) from the fold keys.
// ok is false when the keys do not cover the map.
func (sdb *IntraBlockState) foldKeysDirty() ([]types.Address, bool) {
	if !sdb.foldKeysReady() {
		return nil, false
	}
	fk := sdb.fk
	logged := sortedDistinct(fk.dirtyLog)
	fk.dirtyLog = logged
	out := make([]types.Address, 0, len(fk.written)+len(logged))
	keys := len(fk.written)
	i, j := 0, 0
	for i < len(fk.written) || j < len(logged) {
		c := 1
		switch {
		case i == len(fk.written):
		case j == len(logged):
			c = -1
		default:
			c = compareAddresses(fk.written[i], logged[j])
		}
		if c <= 0 {
			out = append(out, fk.written[i])
			i++
			if c == 0 {
				j++
			}
			continue
		}
		if _, present := sdb.stateObjectsDirty[logged[j]]; present {
			keys++
			out = append(out, logged[j])
		}
		j++
	}
	if keys != len(sdb.stateObjectsDirty) {
		return nil, false
	}
	if foldKeysVerify {
		if want := sortedAddresses(sdb.stateObjectsDirty); !slices.Equal(want, out) {
			panic(fmt.Sprintf("fold keys: dirty set differs (%d vs %d)", len(out), len(want)))
		}
	}
	return out, true
}

// sortedDistinct sorts addrs in place and drops repeats.
func sortedDistinct(addrs []types.Address) []types.Address {
	sortAddresses(addrs)
	return slices.Compact(addrs)
}
