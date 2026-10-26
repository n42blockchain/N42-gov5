// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"os"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// N42_FOLD_NOJOURNAL=1: the parallel processor's account fold
// (ApplyAccounts, ApplyAccountsSharded) on a strict run writes no journal.
//
// The fold's records (createObject, balance and nonce per written account,
// balanceIncrease per credit) are never reverted: a block-end system call
// reverts only its own records, and nothing else snapshots across the
// fold. They existed only for the dirty set the journal holds, which the
// block end's first FinalizeTx walked in full -- the fold's whole dirty
// set, ~31k addresses on the fleet, and again its ~23k pending credits in
// the second -- and the root merged once more.
//
// With the flag on, the fold leaves each written account exactly where
// that FinalizeTx would: live and in stateObjectsDirty, with the flags it
// would set already set, and listed (foldWritten) until a finalize takes
// it, for the finalizes that must see it in the dirty set (a real
// writer's). A prepared object (newObject over its base
// account, or a fresh account for an absent one) is never created or
// self-destructed and never deleted unless empty, so finalizeFlags leaves
// it as it is; an empty one, which finalizeFlags may delete, keeps its
// journal records. A credit keeps its balanceInc entry, which the fold
// marks (BalanceIncrease.fold) and lists (foldCredits): the list stands in
// for the credit's address in the journal's dirty set, as the old
// FinalizeTx left it there (no object, a live credit: kept) or took it
// out (loaded: finalized and dropped).
//
// Every reader of the journal's dirty set treats a listed, marked credit
// as a member of it:
//
//   - FinalizeTx with the no-op writer on a state that keeps its pending
//     credits (the block-end system calls): the credits a load folded
//     since the last finalize (foldTouched) are finalized and dropped as
//     the old walk did, the others are kept;
//   - any other FinalizeTx, SoftFinalise: the marked credits go back into
//     the journal's dirty set first (spillFoldCredits) and the old code
//     runs on it unchanged;
//   - the root's merge, CapturePostState, MakeWriteSet, Print and
//     FinalizeDigestForTest take the marked credits beside the journal's
//     dirty set; a journal reset unmarks them.
//
// Default off: the fold journals as before.
var foldNoJournalOn = parseFinalizeFast(os.Getenv("N42_FOLD_NOJOURNAL"))

// FoldNoJournalOn reports N42_FOLD_NOJOURNAL.
func FoldNoJournalOn() bool { return foldNoJournalOn }

// SetFoldNoJournalForTest switches N42_FOLD_NOJOURNAL and returns the
// restore.
func SetFoldNoJournalForTest(on bool) func() {
	prev := foldNoJournalOn
	foldNoJournalOn = on
	return func() { foldNoJournalOn = prev }
}

// SetFoldNoJournal makes the next account fold on this state write no
// journal (see above); the caller sets it around a strict run's fold only
// and clears it after.
func (sdb *IntraBlockState) SetFoldNoJournal(on bool) { sdb.foldNoJournal = on }

// FoldNoJournal reports SetFoldNoJournal.
func (sdb *IntraBlockState) FoldNoJournal() bool { return sdb.foldNoJournal }

// FoldCredits returns how many credits the no-journal fold left marked.
func (sdb *IntraBlockState) FoldCredits() int {
	n := 0
	for _, bi := range sdb.foldBIs {
		if bi.fold {
			n++
		}
	}
	return n
}

// foldPreparedNoJournal puts a prepared object of the no-journal fold in
// place (see above). False when the object is empty: the caller journals it.
func (sdb *IntraBlockState) foldPreparedNoJournal(addr types.Address, obj *stateObject) bool {
	if obj.empty() {
		return false
	}
	sdb.stateObjects[addr] = obj
	sdb.stateObjectsDirty[addr] = struct{}{}
	sdb.lastAddr, sdb.lastObj = addr, obj
	sdb.foldWritten = append(sdb.foldWritten, addr)
	return true
}

// foldCreditNoJournal is AddBalance for the no-journal fold: an account
// with no live object (and not the zero ripemd credit) is credited through
// its balanceInc entry, with no journal record, and listed; any other
// takes AddBalance.
func (sdb *IntraBlockState) foldCreditNoJournal(addr types.Address, amount *uint256.Int) {
	if _, live := sdb.stateObjects[addr]; live || (addr == ripemd && amount.IsZero()) || sdb.trace || sdb.tracer != nil {
		sdb.AddBalance(addr, amount)
		return
	}
	bi, ok := sdb.balanceInc[addr]
	if !ok {
		if len(sdb.biSlab) > 0 {
			bi = &sdb.biSlab[0]
			sdb.biSlab = sdb.biSlab[1:]
		} else {
			bi = &BalanceIncrease{}
		}
		sdb.balanceInc[addr] = bi
	}
	bi.increase.Add(&bi.increase, amount)
	bi.count++
	sdb.listFoldCredit(addr, bi)
}

// listFoldCredit marks bi (addr's credit) and lists it, once.
func (sdb *IntraBlockState) listFoldCredit(addr types.Address, bi *BalanceIncrease) {
	if bi.fold {
		return
	}
	bi.fold = true
	sdb.foldCredits = append(sdb.foldCredits, addr)
	sdb.foldBIs = append(sdb.foldBIs, bi)
}

// noteFoldTouched records that addr's marked credit was folded into an
// object (foldBalanceIncrease): the next keep-pending FinalizeTx finalizes
// it, as the old one found it in the journal's dirty set with an object.
func (sdb *IntraBlockState) noteFoldTouched(addr types.Address, bi *BalanceIncrease) {
	sdb.foldTouched = append(sdb.foldTouched, addr)
	sdb.foldTouchedBI = append(sdb.foldTouchedBI, bi)
}

// finalizeFoldTouched is a keep-pending no-op FinalizeTx's walk over the
// marked credits that have been loaded since the last one, exactly as the
// old walk (with clearCurrentTxFlags) treated those addresses in the
// journal's dirty set: an object is finalized, marked dirty and dropped;
// no object with a live credit is kept; no object without one is dropped.
// The marked credits never loaded have no object and a live credit: kept.
func (sdb *IntraBlockState) finalizeFoldTouched(policy accountWritePolicy) {
	for i, addr := range sdb.foldTouched {
		bi := sdb.foldTouchedBI[i]
		if !bi.fold {
			continue
		}
		so, exist := sdb.stateObjects[addr]
		if !exist {
			if bi.transferred {
				bi.fold = false
			}
			continue
		}
		finalizeFlags(policy, addr, so)
		sdb.stateObjectsDirty[addr] = struct{}{}
		if !so.deleted {
			so.created = false
			so.selfdestructed = false
		}
		bi.fold = false
	}
	clear(sdb.foldTouchedBI)
	sdb.foldTouched = sdb.foldTouched[:0]
	sdb.foldTouchedBI = sdb.foldTouchedBI[:0]
}

// forEachFoldPending calls fn for every marked credit: the addresses the
// old journal's dirty set would still hold for the fold.
func (sdb *IntraBlockState) forEachFoldPending(fn func(addr types.Address, bi *BalanceIncrease)) {
	for i, bi := range sdb.foldBIs {
		if bi.fold {
			fn(sdb.foldCredits[i], bi)
		}
	}
}

// foldPending reports whether addr stands in the journal's dirty set for
// the no-journal fold: a marked credit, or an account it wrote and no
// finalize has taken yet.
func (sdb *IntraBlockState) foldPending(addr types.Address) bool {
	if len(sdb.foldBIs) != 0 {
		if bi := sdb.balanceInc[addr]; bi != nil && bi.fold {
			return true
		}
	}
	for _, a := range sdb.foldWritten {
		if a == addr {
			return true
		}
	}
	return false
}

// dropFoldCredits unmarks every credit and forgets the written accounts:
// the journal's dirty set they stand in for was reset.
func (sdb *IntraBlockState) dropFoldCredits() {
	sdb.foldWritten = sdb.foldWritten[:0]
	if len(sdb.foldBIs) == 0 && len(sdb.foldTouched) == 0 {
		return
	}
	for _, bi := range sdb.foldBIs {
		bi.fold = false
	}
	clear(sdb.foldBIs)
	clear(sdb.foldTouchedBI)
	sdb.foldCredits = sdb.foldCredits[:0]
	sdb.foldBIs = sdb.foldBIs[:0]
	sdb.foldTouched = sdb.foldTouched[:0]
	sdb.foldTouchedBI = sdb.foldTouchedBI[:0]
}

// spillFoldCredits puts every marked credit and every written account not
// yet finalized back into the journal's dirty set and unmarks them: the state is then the one the journaled fold leaves
// (as far as any reader of the dirty set can tell), for the paths that run
// the old code on it.
func (sdb *IntraBlockState) spillFoldCredits() {
	if len(sdb.foldBIs) == 0 && len(sdb.foldTouched) == 0 && len(sdb.foldWritten) == 0 {
		return
	}
	d := sdb.journal.dirties
	for _, addr := range sdb.foldWritten {
		d[addr]++
	}
	sdb.forEachFoldPending(func(addr types.Address, _ *BalanceIncrease) {
		d[addr]++
	})
	sdb.dropFoldCredits()
}
