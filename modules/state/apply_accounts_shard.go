// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"slices"
	"sync"
)

// The account fold's serial pass, split (N42_COLLECT_SHARD).
//
// ApplyAccounts prepares the written accounts' objects across goroutines
// and leaves, per entry, the live-map insert, the journal records and the
// dirty count -- and every credit's AddBalance (its record, dirty count and
// pending-credit insert) -- to one serial pass in entry order. At 163k
// transfers that pass is ~490k journal records (~55 MB written) and ~650k
// map inserts on one core.
//
// None of those writes depends on another entry's: the addresses are
// distinct, so an entry's outcome is decided by the state before the fold
// and by the entry alone. ApplyAccountsSharded therefore lays each run of
// such entries out ahead (every record's journal position, every new
// credit's record from the reserved slab, in entry order) and then writes
// them with one goroutine per map -- the live objects, the journal's dirty
// counts, the pending credits -- beside goroutines filling the journal by
// index range. Each map ends holding exactly the keys and values the serial
// pass leaves (a map's content does not depend on its insert order), the
// journal the same records in the same order, the slab the same remainder.
// Entries that need the per-entry calls (a live object, an address read
// through the base reader, a remembered absent address, the zero ripemd
// credit) run in place, in order, between the runs.

// Entry kinds for ApplyAccountsSharded.
const (
	shardSerial   uint8 = iota // the per-entry calls, in place
	shardPrepared              // a prepared full entry (preparedAccount)
	shardCredit                // a credit of an account with no live object
	// shardPreparedJournal: a prepared full entry the no-journal fold
	// journals (an empty account, which finalizing may delete), in place.
	shardPreparedJournal
)

// inShardRun reports whether an entry of kind k takes the split pass.
func inShardRun(k uint8) bool { return k == shardPrepared || k == shardCredit }

// applyShardMinRun is the run length below which a run is applied in
// place: its goroutines cost more than they save.
const applyShardMinRun = 1024

// ApplyAccountsSharded applies entries exactly as ApplyAccounts does --
// the same objects, maps, journal records in the same order, dirty counts,
// pending credits and their slab, prefetch hits and last resolved object --
// with the serial pass split across workers goroutines (see above). The
// addresses must be distinct. A state ApplyAccounts would not prepare on
// takes ApplyAccounts.
func (sdb *IntraBlockState) ApplyAccountsSharded(entries []AccountApply, workers int) {
	pf := sdb.accountPrefetch
	if workers < 2 || len(entries) < applyAccountsMinEntries || pf == nil ||
		sdb.stateReader != StateReader(pf) || sdb.trace || sdb.tracer != nil ||
		(sdb.snap != nil && sdb.snap.CanWrite()) {
		sdb.ApplyAccounts(entries, workers)
		return
	}

	n := len(entries)
	prep := make([]preparedAccount, n)
	kind := make([]uint8, n)
	// bis: a credit's pending increase already present before the fold
	// (nil: the credit inserts one).
	bis := make([]*BalanceIncrease, n)
	hits := make([]int, workers)
	per := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > n {
			hi = n
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			hits[w] = sdb.prepareAccounts(pf, entries[lo:hi], prep[lo:hi])
			sdb.classifyShardEntries(entries[lo:hi], prep[lo:hi], kind[lo:hi], bis[lo:hi])
		}(w, lo, hi)
	}
	wg.Wait()
	for _, h := range hits {
		pf.hits += h
	}

	for i := 0; i < n; {
		switch kind[i] {
		case shardSerial:
			sdb.applyAccountEntry(&entries[i])
			i++
			continue
		case shardPreparedJournal:
			sdb.applyPreparedJournaled(&entries[i], &prep[i])
			i++
			continue
		}
		j := i + 1
		for j < n && inShardRun(kind[j]) {
			j++
		}
		if j-i < applyShardMinRun {
			for k := i; k < j; k++ {
				sdb.applyShardEntryInPlace(&entries[k], &prep[k], kind[k])
			}
		} else {
			sdb.applyShardRun(entries[i:j], prep[i:j], kind[i:j], bis[i:j], workers)
		}
		i = j
	}
}

// classifyShardEntries sets each entry's kind (read-only on this state; the
// caller runs it while nothing writes it). A credit takes the split pass
// when AddBalance would only record it: no live object and not the zero
// ripemd credit, which loads the account.
func (sdb *IntraBlockState) classifyShardEntries(entries []AccountApply, prep []preparedAccount, kind []uint8, bis []*BalanceIncrease) {
	for i := range entries {
		e := &entries[i]
		switch {
		case e.Delta != nil:
			if _, live := sdb.stateObjects[e.Addr]; live {
				continue
			}
			if e.Addr == ripemd && e.Delta.IsZero() {
				continue
			}
			kind[i] = shardCredit
			bis[i] = sdb.balanceInc[e.Addr]
		case prep[i].obj != nil:
			kind[i] = shardPrepared
			if sdb.foldNoJournal && prep[i].obj.empty() {
				kind[i] = shardPreparedJournal
			}
		}
	}
}

// applyShardEntryInPlace applies one non-serial entry as ApplyAccounts'
// serial pass does.
func (sdb *IntraBlockState) applyShardEntryInPlace(e *AccountApply, p *preparedAccount, k uint8) {
	if k != shardPrepared {
		if sdb.foldNoJournal && e.Delta != nil {
			sdb.foldCreditNoJournal(e.Addr, e.Delta)
			return
		}
		sdb.applyAccountEntry(e)
		return
	}
	if sdb.foldNoJournal && sdb.foldPreparedNoJournal(e.Addr, p.obj) {
		return
	}
	sdb.applyPreparedJournaled(e, p)
}

// applyPreparedJournaled applies a prepared full entry with its journal
// records, as ApplyAccounts' serial pass does.
func (sdb *IntraBlockState) applyPreparedJournaled(e *AccountApply, p *preparedAccount) {
	addr := e.Addr
	j := sdb.journal
	cnt := 2
	if p.created {
		j.entries = append(j.entries, createObjectChange{account: &addr}.record())
		cnt = 3
	}
	sdb.stateObjects[addr] = p.obj
	sdb.lastAddr, sdb.lastObj = addr, p.obj
	j.entries = append(j.entries,
		balanceChange{account: &addr, prev: p.prevBal}.record(),
		nonceChange{account: &addr, prev: p.prevNonce}.record())
	j.dirties[addr] += cnt
}

// applyShardRun applies a run of prepared and credit entries: the layout
// (record offsets, new credits' records) in entry order, then the maps one
// goroutine each beside the journal fill.
func (sdb *IntraBlockState) applyShardRun(entries []AccountApply, prep []preparedAccount, kind []uint8, bis []*BalanceIncrease, workers int) {
	if sdb.foldNoJournal {
		sdb.applyShardRunNoJournal(entries, prep, kind, bis, workers)
		return
	}
	n := len(entries)
	j := sdb.journal
	// off[i]: entry i's first record, relative to the run's first; a new
	// credit's record comes from the slab, as AddBalance takes it.
	off := make([]int, n)
	fresh := make([]bool, n)
	total := 0
	last := -1
	for i := 0; i < n; i++ {
		off[i] = total
		if kind[i] == shardPrepared {
			total += 2
			if prep[i].created {
				total++
			}
			last = i
			continue
		}
		total++
		if bis[i] == nil {
			if len(sdb.biSlab) > 0 {
				bis[i] = &sdb.biSlab[0]
				sdb.biSlab = sdb.biSlab[1:]
			} else {
				bis[i] = &BalanceIncrease{}
			}
			fresh[i] = true
		}
	}
	base := len(j.entries)
	j.entries = slices.Grow(j.entries, total)[:base+total]
	recs := j.entries[base:]

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := range entries {
			if kind[i] == shardPrepared {
				sdb.stateObjects[entries[i].Addr] = prep[i].obj
			}
		}
	}()
	go func() {
		defer wg.Done()
		d := j.dirties
		for i := range entries {
			cnt := 1
			if kind[i] == shardPrepared {
				cnt = 2
				if prep[i].created {
					cnt = 3
				}
			}
			d[entries[i].Addr] += cnt
		}
	}()
	go func() {
		defer wg.Done()
		inc := sdb.balanceInc
		for i := range entries {
			if fresh[i] {
				inc[entries[i].Addr] = bis[i]
			}
		}
	}()
	fill := workers - 3
	if fill < 1 {
		fill = 1
	}
	per := (n + fill - 1) / fill
	for w := 0; w < fill; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > n {
			hi = n
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				e := &entries[i]
				r := recs[off[i]:]
				if kind[i] == shardPrepared {
					p := &prep[i]
					if p.created {
						r[0] = createObjectChange{account: &e.Addr}.record()
						r = r[1:]
					}
					r[0] = balanceChange{account: &e.Addr, prev: p.prevBal}.record()
					r[1] = nonceChange{account: &e.Addr, prev: p.prevNonce}.record()
					continue
				}
				r[0] = balanceIncrease{account: &e.Addr, increase: *e.Delta}.record()
				bi := bis[i]
				bi.increase.Add(&bi.increase, e.Delta)
				bi.count++
			}
		}(lo, hi)
	}
	wg.Wait()
	if last >= 0 {
		sdb.lastAddr, sdb.lastObj = entries[last].Addr, prep[last].obj
	}
}

// applyShardRunNoJournal is applyShardRun for the no-journal fold
// (fold_nojournal.go): no records; the prepared objects go into the live
// map and stateObjectsDirty, the credits into balanceInc and the fold's
// credit list, which take the same entries in the same order as
// applyShardEntryInPlace's calls would. The run holds no empty prepared
// object (classifyShardEntries leaves those in place).
func (sdb *IntraBlockState) applyShardRunNoJournal(entries []AccountApply, prep []preparedAccount, kind []uint8, bis []*BalanceIncrease, workers int) {
	n := len(entries)
	fresh := make([]bool, n)
	last := -1
	for i := 0; i < n; i++ {
		if kind[i] == shardPrepared {
			sdb.foldWritten = append(sdb.foldWritten, entries[i].Addr)
			last = i
			continue
		}
		bi := bis[i]
		if bi == nil {
			if len(sdb.biSlab) > 0 {
				bi = &sdb.biSlab[0]
				sdb.biSlab = sdb.biSlab[1:]
			} else {
				bi = &BalanceIncrease{}
			}
			bis[i] = bi
			fresh[i] = true
		}
		sdb.listFoldCredit(entries[i].Addr, bi)
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := range entries {
			if kind[i] == shardPrepared {
				sdb.stateObjects[entries[i].Addr] = prep[i].obj
			}
		}
	}()
	go func() {
		defer wg.Done()
		d := sdb.stateObjectsDirty
		for i := range entries {
			if kind[i] == shardPrepared {
				d[entries[i].Addr] = struct{}{}
			}
		}
	}()
	go func() {
		defer wg.Done()
		inc := sdb.balanceInc
		for i := range entries {
			if fresh[i] {
				inc[entries[i].Addr] = bis[i]
			}
		}
	}()
	fill := workers - 3
	if fill < 1 {
		fill = 1
	}
	per := (n + fill - 1) / fill
	for w := 0; w < fill; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > n {
			hi = n
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				if kind[i] != shardCredit {
					continue
				}
				bi := bis[i]
				bi.increase.Add(&bi.increase, entries[i].Delta)
				bi.count++
			}
		}(lo, hi)
	}
	wg.Wait()
	if last >= 0 {
		sdb.lastAddr, sdb.lastObj = entries[last].Addr, prep[last].obj
	}
}
