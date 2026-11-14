// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
)

// The parallel processor's account fold (P13c).
//
// After a Block-STM run the processor replays every final account version
// onto this state: SetBalance+SetNonce for an account the block wrote,
// AddBalance for one it only credited, Selfdestruct for one it deleted.
// At 163k transfers that is ~160k SetBalance/SetNonce pairs, serially, and
// most of their cost is work that mutates nothing shared -- the prefetch
// layer's cache lookup, the object allocation and its fill. ApplyAccounts
// does that part across goroutines and leaves only the map insert and the
// journal records to the serial pass, in entry order.

// AccountApply is one final account version for ApplyAccounts.
type AccountApply struct {
	Addr types.Address
	// Delta, when set, credits the account (AddBalance).
	Delta *uint256.Int
	// Delete, when set (and Delta is not), self-destructs the account.
	Delete bool
	// Balance and Nonce are the account's final values otherwise
	// (SetBalance then SetNonce).
	Balance uint256.Int
	Nonce   uint64
}

// applyAccountsMinEntries is the entry count below which ApplyAccounts
// takes the per-entry calls: the goroutines cost more than they save.
const applyAccountsMinEntries = 2048

// preparedAccount is an object built off the serial pass for a full entry,
// already holding its final balance and nonce, with the values the
// journal records it would have pushed must carry.
type preparedAccount struct {
	obj       *stateObject
	prevBal   uint256.Int
	prevNonce uint64
	created   bool // absent from the base state: createObject's record
}

// ApplyAccounts applies entries in order, exactly as the loop
//
//	for _, e := range entries {
//		switch {
//		case e.Delta != nil: sdb.AddBalance(e.Addr, e.Delta)
//		case e.Delete:       sdb.Selfdestruct(e.Addr)
//		default:             sdb.SetBalance(e.Addr, &e.Balance); sdb.SetNonce(e.Addr, e.Nonce)
//		}
//	}
//
// would: the same objects, maps, journal records in the same order, dirty
// counts and prefetch hits. The addresses must be distinct.
//
// A full entry is prepared across workers goroutines when its outcome
// depends on nothing the earlier entries can change: the address has no
// live object, is not the last resolved one, is not remembered absent,
// owes no pending credit, and the prefetch layer -- this state's reader
// itself, so no recorder or tracer above it observes the read -- answers
// it from its seeded set or its known cache, both safe for concurrent
// reads. Distinct addresses make each of those conditions a property of
// the entry alone: no other entry's call touches this address's maps. Any
// other entry, and every entry of a state with a tracer, a writable
// snapshot or no such layer, takes the per-entry calls in place.
func (sdb *IntraBlockState) ApplyAccounts(entries []AccountApply, workers int) {
	pf := sdb.accountPrefetch
	if workers < 2 || len(entries) < applyAccountsMinEntries || pf == nil ||
		sdb.stateReader != StateReader(pf) || sdb.trace || sdb.tracer != nil ||
		(sdb.snap != nil && sdb.snap.CanWrite()) {
		for i := range entries {
			sdb.applyAccountEntry(&entries[i])
		}
		return
	}

	prep := make([]preparedAccount, len(entries))
	hits := make([]int, workers)
	per := (len(entries) + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > len(entries) {
			hi = len(entries)
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			hits[w] = sdb.prepareAccounts(pf, entries[lo:hi], prep[lo:hi])
		}(w, lo, hi)
	}
	wg.Wait()
	// The prepared reads are the prefetch hits the per-entry calls count.
	for _, h := range hits {
		pf.hits += h
	}

	nj := sdb.foldNoJournal
	for i := range entries {
		p := &prep[i]
		if p.obj == nil {
			if nj && entries[i].Delta != nil {
				sdb.foldCreditNoJournal(entries[i].Addr, entries[i].Delta)
				continue
			}
			sdb.applyAccountEntry(&entries[i])
			continue
		}
		// N42_FOLD_NOJOURNAL: the object where the block end's FinalizeTx
		// would leave it, without the records (fold_nojournal.go).
		if nj && sdb.foldPreparedNoJournal(entries[i].Addr, p.obj) {
			continue
		}
		// What SetBalance+SetNonce leave on a miss of every live map:
		// getStateObject's load (for an absent account: createObject's
		// record; the nilAccounts mark it sets is deleted again by
		// setStateObject), the insert, the balance and nonce records. The
		// fold setStateObject runs is a no-op: no pending credit.
		// The records go in as push would append them; their dirty counts
		// (each of them dirties addr) are added in one map write.
		addr := entries[i].Addr
		j := sdb.journal
		n := 2
		if p.created {
			j.entries = append(j.entries, createObjectChange{account: &addr}.record())
			n = 3
		}
		sdb.stateObjects[addr] = p.obj
		sdb.lastAddr, sdb.lastObj = addr, p.obj
		j.entries = append(j.entries,
			balanceChange{account: &addr, prev: p.prevBal}.record(),
			nonceChange{account: &addr, prev: p.prevNonce}.record())
		j.dirties[addr] += n
	}
}

// applyAccountEntry is one entry's per-entry calls.
func (sdb *IntraBlockState) applyAccountEntry(e *AccountApply) {
	switch {
	case e.Delta != nil:
		sdb.AddBalance(e.Addr, e.Delta)
	case e.Delete:
		sdb.Selfdestruct(e.Addr)
	default:
		sdb.SetBalance(e.Addr, &e.Balance)
		sdb.SetNonce(e.Addr, e.Nonce)
	}
}

// prepareAccounts builds the objects of the full entries that qualify (see
// ApplyAccounts) into prep and returns how many prefetch reads it made. It
// only reads this state's maps; the caller runs it while nothing writes
// them.
func (sdb *IntraBlockState) prepareAccounts(pf *AccountPrefetch, entries []AccountApply, prep []preparedAccount) int {
	hits := 0
	var empty account.StateAccount
	// N42_AHEAD_COLLECT_CUT: the objects come from one slab per call (see
	// foldObjectSlab), sized for this call's full entries.
	var slab []stateObject
	if foldObjectSlab {
		full := 0
		for i := range entries {
			if entries[i].Delta == nil && !entries[i].Delete {
				full++
			}
		}
		slab = make([]stateObject, full)
	}
	newObj := func(addr types.Address, data, original *account.StateAccount) *stateObject {
		if len(slab) > 0 {
			so := &slab[0]
			slab = slab[1:]
			return initObject(so, sdb, addr, data, original)
		}
		return newObject(sdb, addr, data, original)
	}
	for i := range entries {
		e := &entries[i]
		if e.Delta != nil || e.Delete {
			continue
		}
		addr := e.Addr
		if _, live := sdb.stateObjects[addr]; live {
			continue
		}
		if sdb.lastObj != nil && sdb.lastAddr == addr {
			continue
		}
		if _, absent := sdb.nilAccounts[addr]; absent {
			continue
		}
		if len(sdb.balanceInc) != 0 {
			if _, pending := sdb.balanceInc[addr]; pending {
				continue
			}
		}
		acc, ok := pf.lookupSeeded(addr)
		if !ok {
			continue // a read of the base reader: serial, in order
		}
		hits++
		p := &prep[i]
		if acc == nil {
			// createObject(addr, nil): a zero account over a zero original,
			// its nonce set (to 0) to mark it dirty.
			p.obj = newObj(addr, &empty, &empty)
			p.obj.setNonce(0)
			p.created = true
		} else {
			p.obj = newObj(addr, acc, acc)
		}
		p.prevBal = p.obj.data.Balance
		p.obj.setBalance(&e.Balance)
		p.prevNonce = p.obj.data.Nonce
		p.obj.setNonce(e.Nonce)
	}
	return hits
}

// foldObjectSlab is N42_AHEAD_COLLECT_CUT (default off, r68a): the account
// fold's prepared objects (prepareAccounts, on the parallel collect of
// every Block-STM run) are carved from one slab per worker instead of
// taken one by one from stateObjectPool. A block's state is never Reset on
// the node's import and build paths, so nothing is ever put back and every
// Get misses: sync.Pool's slow path then probes every P's shared chain and
// victim cache before New -- O(GOMAXPROCS) per object, run by every fold
// worker at once (a 52.5k-transfer fold at 32 Ps spent ~77% of its
// preparation there). The object a slab entry becomes is newObject's on a
// zero struct -- exactly the struct New returns, and what
// N42_STATE_OBJECT_POOL=off allocates -- so the state is the same.
var foldObjectSlab = envswitch.Is1("N42_AHEAD_COLLECT_CUT", envswitch.Perf, "Account fold object slab: prepared objects of the fold share one slab (r68a).")

// SetFoldObjectSlabForTest switches the fold's object slab and returns the
// restore.
func SetFoldObjectSlabForTest(on bool) func() {
	prev := foldObjectSlab
	foldObjectSlab = on
	return func() { foldObjectSlab = prev }
}

// FoldObjectSlabOn reports N42_AHEAD_COLLECT_CUT's fold object slab.
func FoldObjectSlabOn() bool { return foldObjectSlab }
