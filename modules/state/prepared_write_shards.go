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
func (sdb *IntraBlockState) commitBlockShards(chainRules *params.Rules, shards int) ([]*StateWriteLog, error) {
	for _, addr := range sdb.pendingIncreaseAddrs() {
		sdb.getStateObject(addr)
	}
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
			l := &StateWriteLog{}
			l.reserveAccountWrites(len(addrs))
			for _, addr := range addrs {
				_, isDirty := sdb.stateObjectsDirty[addr]
				_, needsWipe := sdb.storageWipes[addr]
				if err := updateAccountWithWipe(policy, l, addr, sdb.stateObjects[addr], isDirty, needsWipe); err != nil {
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
	sdb.clearJournalAndRefund()
	return logs, nil
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
	if shards < 2 || len(ibs.stateObjects) < prepShardMinObjects {
		return nil, false, nil
	}
	logs, err := ibs.commitBlockShards(rules, shards)
	if err != nil {
		return nil, true, err
	}
	merged := func() *StateWriteLog {
		l := &StateWriteLog{}
		for _, s := range logs {
			l.appendLog(s)
		}
		return l
	}
	if number == 0 || !(HistoryIndexDisabled() || HistoryIndexDeferred()) {
		return &PreparedStateWrite{log: merged()}, true, nil
	}
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
	// The calls are kept only for the transaction's check of created
	// addresses (WriteState).
	for _, l := range logs {
		if l.NeedsStore() {
			p.log = merged()
			break
		}
	}
	return p, true, nil
}
