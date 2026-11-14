// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package state

import (
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// A prepared state write split by address (E1N26 run 16).
//
// With the recipients spread over all 22857 accounts a full transfer block
// changes ~31k accounts, and its prepared state write -- CommitBlock into a
// log, the log replayed into plain rows and changesets, the changesets
// encoded -- took ~68 ms on node0, longer than a block cycle, so every
// import waited for the background writer. Every step works on one account
// at a time and emits rows in address order, so the accounts are split into
// shards by their first byte (contiguous address ranges, ascending): each
// shard sorts its addresses, records its accounts' writes, replays and
// encodes them on its own goroutine, and the shards' logs, rows and
// changeset rows concatenated in shard order are exactly the serial ones.
// What reads or folds shared state -- the pending balance increases'
// object loads, the dirty set, the journal reset -- stays serial.
//
// N42_STATE_WRITE_PREP_SHARDS sets the shard count (default 8; 0 or 1:
// serial). Blocks with fewer than prepShardMinObjects objects stay serial.

var prepShards = parsePrepShards(os.Getenv("N42_STATE_WRITE_PREP_SHARDS"))

// prepDeferLoads is N42_STATE_WRITE_PREP_DEFER_LOADS (default on; "0" off,
// E1N26 run 18). Under the no-journal fold an import's ~23k credited
// recipients stay pending until the write, and making each one's object
// live -- the read, the object, the live-set insert, the fold and its
// journal record -- took ~0.94 µs serially: ~21 ms of a 35 ms sharded
// write in the benchmark, more on node0. With it on only the reads stay
// serial; each shard builds its credited accounts' objects itself.
var prepDeferLoads = os.Getenv("N42_STATE_WRITE_PREP_DEFER_LOADS") != "0"

// SetPrepDeferLoadsForTest switches the deferred credited loads and
// returns the restore.
func SetPrepDeferLoadsForTest(on bool) func() {
	prev := prepDeferLoads
	prepDeferLoads = on
	return func() { prepDeferLoads = prev }
}

func parsePrepShards(v string) int {
	if v == "" {
		return 8
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 1
	}
	if n > 256 {
		return 256
	}
	return n
}

// SetPrepShardsForTest sets the shard count and returns the restore.
func SetPrepShardsForTest(n int) func() {
	prev := prepShards
	prepShards = n
	return func() { prepShards = prev }
}

const prepShardMinObjects = 4096

// commitBlockShards is CommitBlock into one StateWriteLog per address
// shard: shard k holds the accounts whose first byte b has b*shards/256 ==
// k, in address order, so the logs in shard order are CommitBlock's log.
func (sdb *IntraBlockState) commitBlockShards(chainRules *params.Rules, shards int, ph *PrepPhases) ([]*StateWriteLog, error) {
	t0 := time.Now()
	if prepLoadPar > 1 && prepDeferLoads && sdb.accountPrefetch != nil && sdb.stateReader == StateReader(sdb.accountPrefetch) {
		return sdb.commitBlockShardsParLoad(chainRules, shards, ph, t0)
	}
	pending := sdb.pendingIncreaseAddrs()
	var deferred [][]creditedLoad
	if prepDeferLoads {
		deferred = make([][]creditedLoad, shards)
	}
	for _, addr := range pending {
		if _, live := sdb.stateObjects[addr]; live || deferred == nil {
			if !live {
				ph.Loaded++
			}
			sdb.getStateObject(addr)
			continue
		}
		ph.Loaded++
		// getStateObject's read, without the live object: the shard
		// builds it (creditedObject).
		var acc *account.StateAccount
		if _, absent := sdb.nilAccounts[addr]; !absent {
			a, err := sdb.stateReader.ReadAccountData(addr)
			if err != nil {
				return nil, err
			}
			if a != nil && sdb.snap != nil && sdb.snap.CanWrite() {
				sdb.snap.AddAccount(addr, a)
			}
			acc = a
		}
		k := int(addr[0]) * shards / 256
		deferred[k] = append(deferred[k], creditedLoad{addr: addr, acc: acc, bi: sdb.balanceInc[addr]})
	}
	ph.Pending = len(pending)
	return sdb.commitBlockShardsLoaded(chainRules, shards, ph, t0, deferred)
}

// commitBlockShardsLoaded is commitBlockShards after the pending loads:
// deferred holds each shard's credited loads in address order (nil when
// the loads are not deferred).
func (sdb *IntraBlockState) commitBlockShardsLoaded(chainRules *params.Rules, shards int, ph *PrepPhases, t0 time.Time, deferred [][]creditedLoad) ([]*StateWriteLog, error) {
	t1 := time.Now()
	ph.Load = t1.Sub(t0)
	for addr := range sdb.journal.dirties {
		sdb.stateObjectsDirty[addr] = struct{}{}
	}
	sdb.forEachFoldPending(func(addr types.Address, _ *BalanceIncrease) {
		sdb.stateObjectsDirty[addr] = struct{}{}
	})
	buckets := make([][]types.Address, shards)
	per := len(sdb.stateObjects)/shards + 1
	for k := range buckets {
		buckets[k] = make([]types.Address, 0, per+per/4)
	}
	for addr := range sdb.stateObjects {
		k := int(addr[0]) * shards / 256
		buckets[k] = append(buckets[k], addr)
	}
	t2 := time.Now()
	ph.Mark = t2.Sub(t1)
	policy := newAccountWritePolicy(chainRules)
	logs := make([]*StateWriteLog, shards)
	errs := make([]error, shards)
	var wg sync.WaitGroup
	for k := range buckets {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			addrs := buckets[k]
			sortAddresses(addrs)
			var credited []creditedLoad
			if deferred != nil {
				credited = deferred[k] // in address order, as pending is
			}
			l := &StateWriteLog{}
			l.reserveAccountWrites(len(addrs) + len(credited))
			// The live and the credited addresses are disjoint; merged in
			// address order.
			for len(addrs) > 0 || len(credited) > 0 {
				var (
					addr    types.Address
					obj     *stateObject
					isDirty bool
				)
				if len(credited) == 0 || (len(addrs) > 0 && compareAddresses(addrs[0], credited[0].addr) < 0) {
					addr, addrs = addrs[0], addrs[1:]
					obj = sdb.stateObjects[addr]
					_, isDirty = sdb.stateObjectsDirty[addr]
				} else {
					c := credited[0]
					credited = credited[1:]
					// The fold's journal entry made it dirty.
					addr, obj, isDirty = c.addr, sdb.creditedObject(c), true
				}
				_, needsWipe := sdb.storageWipes[addr]
				if err := updateAccountWithWipe(policy, l, addr, obj, isDirty, needsWipe); err != nil {
					errs[k] = err
					return
				}
			}
			logs[k] = l
		}(k)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	t3 := time.Now()
	ph.Commit = t3.Sub(t2)
	sdb.clearJournalAndRefund()
	ph.Reset = time.Since(t3)
	return logs, nil
}

// creditedLoad is a pending balance increase's account read for the
// sharded write: acc is the stored account, nil when there is none.
type creditedLoad struct {
	addr types.Address
	acc  *account.StateAccount
	bi   *BalanceIncrease
}

// creditedObject is the object getStateObject would make live for c and
// fold c's increase into (createObject for an absent account), built off
// the live set: the write is the block's last use of the state, so only
// the written values have to match. The object's journal records and the
// fold's touched list, which the write's journal reset drops, are skipped.
func (sdb *IntraBlockState) creditedObject(c creditedLoad) *stateObject {
	var obj *stateObject
	if c.acc == nil {
		obj = newObject(sdb, c.addr, new(account.StateAccount), &account.StateAccount{})
		obj.setNonce(0)
	} else {
		obj = newObject(sdb, c.addr, c.acc, c.acc)
	}
	obj.data.Balance.Add(&obj.data.Balance, &c.bi.increase)
	c.bi.transferred = true
	return obj
}

// PrepPhases times a sharded prepared write's steps: Load materialises the
// pending balance increases' objects (Pending addresses, Loaded of them not
// yet live), Mark builds the dirty set and the shards, Commit records the
// shards' writes, Reset clears the journal, Encode replays and encodes the
// shards, Merge concatenates them. Zero for a serial or slow-path write.
type PrepPhases struct {
	Load, Mark, Commit, Reset, Encode, Merge time.Duration
	// Tail is the step after Merge: keeping the calls for the created
	// addresses' check (the merged log, or only the addresses).
	Tail            time.Duration
	Pending, Loaded int
}

// appendLog appends o's calls after l's.
func (l *StateWriteLog) appendLog(o *StateWriteLog) {
	na, nc, ns, ncr := int32(len(l.accts)), int32(len(l.codes)), int32(len(l.slots)), int32(len(l.creates))
	for _, op := range o.ops {
		switch op.kind {
		case opUpdateAccount, opDeleteAccount:
			op.idx += na
		case opUpdateCode:
			op.idx += nc
		case opWriteStorage:
			op.idx += ns
		case opCreateContract:
			op.idx += ncr
		}
		l.ops = append(l.ops, op)
	}
	l.accts = append(l.accts, o.accts...)
	l.codes = append(l.codes, o.codes...)
	l.slots = append(l.slots, o.slots...)
	l.creates = append(l.creates, o.creates...)
}

// preparedShard is one shard's part of a prepared write.
type preparedShard struct {
	rows           stateRows
	acct, stor     []ChangeSetRow
	nAccts, nSlots int
	wroteBefore    bool
	err            error
}

// prepareStateWriteShards is PrepareStateWrite's sharded run. ok is false
// when the block is too small to split (the caller runs serially).
func prepareStateWriteShards(ibs *IntraBlockState, rules *params.Rules, number uint64, shards int) (p *PreparedStateWrite, ok bool, err error) {
	if shards < 2 || len(ibs.stateObjects)+len(ibs.balanceInc) < prepShardMinObjects {
		return nil, false, nil
	}
	var ph PrepPhases
	logs, err := ibs.commitBlockShards(rules, shards, &ph)
	if err != nil {
		return nil, true, err
	}
	merged := func() *StateWriteLog { return mergeShardLogs(logs) }
	if number == 0 || !(HistoryIndexDisabled() || HistoryIndexDeferred()) {
		return &PreparedStateWrite{log: merged()}, true, nil
	}
	tEnc := time.Now()
	parts := make([]preparedShard, shards)
	var wg sync.WaitGroup
	for k := range logs {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			sh := &parts[k]
			pw := &PlainStateWriter{db: &sh.rows, csw: NewChangeSetWriterPlain(nil, number)}
			cw := &createCheckWriter{PlainStateWriter: pw, rows: &sh.rows}
			if sh.err = logs[k].Replay(cw); sh.err != nil || cw.wroteBefore {
				sh.wroteBefore = cw.wroteBefore
				return
			}
			sh.acct, sh.stor, sh.err = pw.csw.EncodeChangeSets()
			sh.nAccts, sh.nSlots = pw.csw.ChangedCounts()
		}(k)
	}
	wg.Wait()
	tMerge := time.Now()
	ph.Encode = tMerge.Sub(tEnc)
	var nRows, nAcct, nStor int
	for k := range parts {
		if parts[k].err != nil {
			return nil, true, parts[k].err
		}
		if parts[k].wroteBefore {
			return &PreparedStateWrite{log: merged()}, true, nil
		}
		nRows += len(parts[k].rows.rows)
		nAcct += len(parts[k].acct)
		nStor += len(parts[k].stor)
	}
	p = &PreparedStateWrite{}
	p.rows.rows = make([]stateRow, 0, nRows)
	p.acct = make([]ChangeSetRow, 0, nAcct)
	if nStor > 0 {
		p.stor = make([]ChangeSetRow, 0, nStor)
	}
	for k := range parts {
		p.rows.rows = append(p.rows.rows, parts[k].rows.rows...)
		p.acct = append(p.acct, parts[k].acct...)
		p.stor = append(p.stor, parts[k].stor...)
		p.nAccts += parts[k].nAccts
		p.nSlots += parts[k].nSlots
	}
	p.fast = true
	tTail := time.Now()
	ph.Merge = tTail.Sub(tMerge)
	// The calls are kept only for the transaction's check of created
	// addresses (WriteState).
	for _, l := range logs {
		if l.NeedsStore() {
			if prepLazyLog {
				// Only the created addresses now; the calls are merged if
				// the check refuses the fast path.
				p.shardLogs = logs
				for _, sl := range logs {
					p.creates = append(p.creates, sl.creates...)
				}
			} else {
				p.log = merged()
			}
			break
		}
	}
	ph.Tail = time.Since(tTail)
	p.phases = ph
	return p, true, nil
}

// mergeShardLogs is the shards' logs concatenated in shard order: the
// serial CommitBlock's log.
func mergeShardLogs(logs []*StateWriteLog) *StateWriteLog {
	l := &StateWriteLog{}
	for _, s := range logs {
		l.appendLog(s)
	}
	return l
}

// prepLazyLog is N42_STATE_WRITE_PREP_LAZY_LOG (default off, r87b). A
// sharded preparation whose calls include a CreateContract kept the calls,
// merged into one log, for the write transaction's check that the created
// addresses hold no Storage row -- a copy of every account write (~31k
// of 240 bytes in a full transfer block, appended without a reserve) on
// the preparation's critical path, the untimed ~12 ms between its Merge
// step and its end (run 67, node0). With it on the preparation keeps only
// the shards' logs and their created addresses; the merged log is built
// in WriteState only when the check refuses the fast path, which then
// replays it exactly as before. The rows written are the same either way.
var prepLazyLog = os.Getenv("N42_STATE_WRITE_PREP_LAZY_LOG") == "1"

// SetPrepLazyLogForTest switches the lazy merged log and returns the
// restore.
func SetPrepLazyLogForTest(on bool) func() {
	prev := prepLazyLog
	prepLazyLog = on
	return func() { prepLazyLog = prev }
}
