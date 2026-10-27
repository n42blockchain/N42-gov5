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
	"bytes"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

// A block's CommitBlock taken off its write transaction
// (docs/QS_MULTI_MILLION_DESIGN.md P11).
//
// CommitBlock talks to its writer only through the StateWriter calls, so it
// can run against a StateWriteLog before the block's MDBX transaction opens
// (on the ordered writer: while the previous block's write is still in
// flight). Inside the transaction the log is then either replayed through
// the block's real writer -- the same calls in the same order, so the same
// rows -- or, when no call needs the store, applied as rows that were
// already produced off the transaction by a PlainStateWriter over a row
// buffer: the plain puts and the encoded, sorted changeset rows.
//
// CreateContract is the one call that reads the store: the pre-wipe slot
// enumeration and the wipe scan the Storage table, which holds the block's
// parent state plus the slots this block's earlier calls wrote. Over a row
// buffer (no cursor) it reads nothing, which is the same result exactly
// when the address has no Storage row at that point: none earlier in the
// block's rows (checked when preparing) and none in the store (checked in
// the transaction, before any prepared row is applied). A prepared write
// whose store check fails is replayed. Every Prague block takes this path:
// the system calls' sender is touched, empty, and removed (EIP-161).

type stateWriteOpKind uint8

const (
	opUpdateAccount stateWriteOpKind = iota
	opUpdateCode
	opDeleteAccount
	opWriteStorage
	opCreateContract
)

type stateWriteOp struct {
	kind stateWriteOpKind
	idx  int32 // into the log's slice for kind
}

type stateWriteAccount struct {
	addr     types.Address
	origNil  bool
	original account.StateAccount
	account  account.StateAccount
}

type stateWriteSlot struct {
	addr            types.Address
	key             types.Hash
	original, value uint256.Int
}

type stateWriteCode struct {
	addr types.Address
	hash types.Hash
	code []byte
}

// StateWriteLog is a StateWriter that records every call, by value, for a
// later Replay.
type StateWriteLog struct {
	ops     []stateWriteOp
	accts   []stateWriteAccount
	slots   []stateWriteSlot
	codes   []stateWriteCode
	creates []types.Address
}

var _ StateWriter = (*StateWriteLog)(nil)

// accountWriteReserver is a writer that can size itself for n account
// writes before MakeWriteSet makes them.
type accountWriteReserver interface {
	reserveAccountWrites(n int)
}

// reserveAccountWrites grows the log for n more account writes at once:
// appended one by one, a block's ~180k accounts (240 bytes each) were
// copied through every doubling, ~75 bytes per imported transaction.
func (l *StateWriteLog) reserveAccountWrites(n int) {
	if n <= 0 {
		return
	}
	if need := len(l.accts) + n; cap(l.accts) < need {
		l.accts = append(make([]stateWriteAccount, 0, need), l.accts...)
	}
	if need := len(l.ops) + n; cap(l.ops) < need {
		l.ops = append(make([]stateWriteOp, 0, need), l.ops...)
	}
}

func (l *StateWriteLog) addAccount(kind stateWriteOpKind, addr types.Address, original, acct *account.StateAccount) {
	a := stateWriteAccount{addr: addr, origNil: original == nil}
	if original != nil {
		a.original = *original
	}
	if acct != nil {
		a.account = *acct
	}
	l.ops = append(l.ops, stateWriteOp{kind: kind, idx: int32(len(l.accts))})
	l.accts = append(l.accts, a)
}

func (l *StateWriteLog) UpdateAccountData(address types.Address, original, acct *account.StateAccount) error {
	l.addAccount(opUpdateAccount, address, original, acct)
	return nil
}

func (l *StateWriteLog) DeleteAccount(address types.Address, original *account.StateAccount) error {
	l.addAccount(opDeleteAccount, address, original, nil)
	return nil
}

func (l *StateWriteLog) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	l.ops = append(l.ops, stateWriteOp{kind: opUpdateCode, idx: int32(len(l.codes))})
	l.codes = append(l.codes, stateWriteCode{addr: address, hash: codeHash, code: code})
	return nil
}

func (l *StateWriteLog) WriteAccountStorage(address types.Address, key types.Hash, original, value uint256.Int) error {
	l.ops = append(l.ops, stateWriteOp{kind: opWriteStorage, idx: int32(len(l.slots))})
	l.slots = append(l.slots, stateWriteSlot{addr: address, key: key, original: original, value: value})
	return nil
}

func (l *StateWriteLog) CreateContract(address types.Address) error {
	l.ops = append(l.ops, stateWriteOp{kind: opCreateContract, idx: int32(len(l.creates))})
	l.creates = append(l.creates, address)
	return nil
}

// NeedsStore reports whether a recorded call reads the store when it is
// applied (CreateContract), so the log must be replayed inside the write
// transaction.
func (l *StateWriteLog) NeedsStore() bool { return len(l.creates) > 0 }

// Replay makes the recorded calls on w, in order.
func (l *StateWriteLog) Replay(w StateWriter) error {
	return l.replayOps(w, l.ops)
}

// opAddr is the address op is a call for.
func (l *StateWriteLog) opAddr(op stateWriteOp) types.Address {
	switch op.kind {
	case opUpdateAccount, opDeleteAccount:
		return l.accts[op.idx].addr
	case opUpdateCode:
		return l.codes[op.idx].addr
	case opWriteStorage:
		return l.slots[op.idx].addr
	default:
		return l.creates[op.idx]
	}
}

// replayOps makes the recorded calls ops (a part of l.ops) on w, in order.
func (l *StateWriteLog) replayOps(w StateWriter, ops []stateWriteOp) error {
	for _, op := range ops {
		var err error
		switch op.kind {
		case opUpdateAccount, opDeleteAccount:
			a := &l.accts[op.idx]
			var orig *account.StateAccount
			if !a.origNil {
				orig = &a.original
			}
			if op.kind == opUpdateAccount {
				err = w.UpdateAccountData(a.addr, orig, &a.account)
			} else {
				err = w.DeleteAccount(a.addr, orig)
			}
		case opUpdateCode:
			c := &l.codes[op.idx]
			err = w.UpdateAccountCode(c.addr, c.hash, c.code)
		case opWriteStorage:
			s := &l.slots[op.idx]
			err = w.WriteAccountStorage(s.addr, s.key, s.original, s.value)
		case opCreateContract:
			err = w.CreateContract(l.creates[op.idx])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// stateRows buffers a writer's puts and deletes for a later transaction.
type stateRows struct {
	rows []stateRow
}

type stateRow struct {
	table      string
	key, value []byte
	del        bool
}

func (r *stateRows) Put(table string, k, v []byte) error {
	r.rows = append(r.rows, stateRow{table: table, key: k, value: v})
	return nil
}

func (r *stateRows) Delete(table string, k []byte) error {
	r.rows = append(r.rows, stateRow{table: table, key: k, del: true})
	return nil
}

func (r *stateRows) replay(tx kv.RwTx) error {
	for _, row := range r.rows {
		var err error
		if row.del {
			err = tx.Delete(row.table, row.key)
		} else {
			err = tx.Put(row.table, row.key, row.value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// PreparedStateWrite is a block's CommitBlock run before its write
// transaction. Use it in place of CommitBlock + WriteChangeSets +
// WriteHistory on the block's PlainStateWriter: WriteState, then
// WriteChangeSets, then WriteHistory.
type PreparedStateWrite struct {
	log *StateWriteLog
	// fast: the rows below are the block's whole state write.
	fast       bool
	rows       stateRows
	acct, stor []ChangeSetRow
	nAccts     int
	nSlots     int
	// Commit and Encode split the preparation (observability): CommitBlock
	// into the log, then the rows and changesets built from it; Shards is
	// how many parts built them (1: serial).
	Commit, Encode time.Duration
	Shards         int
}

// prepShards is N42_LAG_WRITE_PREP_SHARDS: with k > 1, PrepareStateWrite
// builds the plain rows and changesets from the CommitBlock log in up to k
// parts at once, each a run of whole addresses in the log's (sorted) order,
// and joins them in that order: the same rows in the same order, the same
// changeset rows. Default 0 (off): one serial pass, as before.
var prepShards = parsePrepShards(os.Getenv("N42_LAG_WRITE_PREP_SHARDS"))

func parsePrepShards(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 2 {
		return 0
	}
	if n > 64 {
		n = 64
	}
	return n
}

// SetPrepShardsForTest sets N42_LAG_WRITE_PREP_SHARDS and the fewest calls
// a part takes (minOps <= 0 keeps it), and returns the restore.
func SetPrepShardsForTest(n, minOps int) func() {
	prev, prevMin := prepShards, prepShardMinOps
	prepShards = n
	if minOps > 0 {
		prepShardMinOps = minOps
	}
	return func() { prepShards, prepShardMinOps = prev, prevMin }
}

// PrepShards returns N42_LAG_WRITE_PREP_SHARDS (0: off).
func PrepShards() int { return prepShards }

// preparedSharded counts the preparations built in parts.
var preparedSharded atomic.Uint64

// PreparedShardedWrites returns how many state writes were prepared in
// parts (since start).
func PreparedShardedWrites() uint64 { return preparedSharded.Load() }

// prepShardMinOps is the fewest calls a part takes: below it the serial
// pass is cheaper than the goroutines.
var prepShardMinOps = 4096

// PrepareStateWrite runs ibs.CommitBlock against a StateWriteLog and, when
// no call needs the store and the history index is not written per block,
// produces the plain rows and the encoded changesets of block number. It
// owns ibs while it runs, as the write would.
func PrepareStateWrite(ibs *IntraBlockState, rules *params.Rules, number uint64) (*PreparedStateWrite, error) {
	t0 := time.Now()
	log := &StateWriteLog{}
	if err := ibs.CommitBlock(rules, log); err != nil {
		return nil, err
	}
	p := &PreparedStateWrite{log: log, Shards: 1}
	p.Commit = time.Since(t0)
	if number == 0 || !(HistoryIndexDisabled() || HistoryIndexDeferred()) {
		return p, nil
	}
	t1 := time.Now()
	defer func() { p.Encode = time.Since(t1) }()
	if parts := log.shardOps(prepShards, prepShardMinOps); len(parts) > 1 {
		return p, p.encodeShards(log, parts, number)
	}
	pw := &PlainStateWriter{db: &p.rows, csw: NewChangeSetWriterPlain(nil, number)}
	cw := &createCheckWriter{PlainStateWriter: pw, rows: &p.rows}
	if err := log.Replay(cw); err != nil {
		return nil, err
	}
	if cw.wroteBefore {
		return p, nil
	}
	acct, stor, err := pw.csw.EncodeChangeSets()
	if err != nil {
		return nil, err
	}
	p.acct, p.stor = acct, stor
	p.nAccts, p.nSlots = pw.csw.ChangedCounts()
	p.fast = true
	if !log.NeedsStore() {
		p.log = nil
	}
	return p, nil
}

// shardOps splits l.ops into at most k runs of at least minOps calls,
// each ending at an address boundary, or returns nil (one serial pass):
// k < 2, too few calls, or calls of one address not contiguous (the log's
// addresses must not decrease, which CommitBlock's sorted walk gives).
func (l *StateWriteLog) shardOps(k, minOps int) [][]stateWriteOp {
	n := len(l.ops)
	if k < 2 || n < 2*minOps {
		return nil
	}
	if k > n/minOps {
		k = n / minOps
	}
	var prev types.Address
	for i, op := range l.ops {
		a := l.opAddr(op)
		if i > 0 && bytes.Compare(a[:], prev[:]) < 0 {
			return nil
		}
		prev = a
	}
	parts := make([][]stateWriteOp, 0, k)
	start := 0
	for s := 1; s < k && start < n; s++ {
		cut := start + (n-start)/(k-s+1)
		if cut <= start {
			cut = start + 1
		}
		// Move the cut past the calls of the address before it.
		for cut < n && l.opAddr(l.ops[cut]) == l.opAddr(l.ops[cut-1]) {
			cut++
		}
		if cut >= n {
			break
		}
		parts = append(parts, l.ops[start:cut])
		start = cut
	}
	parts = append(parts, l.ops[start:])
	if len(parts) < 2 {
		return nil
	}
	return parts
}

// prepPart is one part's rows and changesets.
type prepPart struct {
	rows        stateRows
	acct, stor  []ChangeSetRow
	nAccts      int
	nSlots      int
	wroteBefore bool
	err         error
}

// encodeShards builds p's rows and changesets from log's calls in parts,
// one goroutine each, and joins them in order. Every address's calls are in
// one part, so a part's CreateContract check sees every earlier Storage row
// of its address, and the parts' changesets are disjoint runs of the sorted
// whole.
func (p *PreparedStateWrite) encodeShards(log *StateWriteLog, parts [][]stateWriteOp, number uint64) error {
	p.Shards = len(parts)
	out := make([]prepPart, len(parts))
	var wg sync.WaitGroup
	for i := range parts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := &out[i]
			pw := &PlainStateWriter{db: &r.rows, csw: NewChangeSetWriterPlain(nil, number)}
			cw := &createCheckWriter{PlainStateWriter: pw, rows: &r.rows}
			if r.err = log.replayOps(cw, parts[i]); r.err != nil {
				return
			}
			if r.wroteBefore = cw.wroteBefore; r.wroteBefore {
				return
			}
			r.acct, r.stor, r.err = pw.csw.EncodeChangeSets()
			r.nAccts, r.nSlots = pw.csw.ChangedCounts()
		}(i)
	}
	wg.Wait()
	nRows, nAcct, nStor := 0, 0, 0
	for i := range out {
		if out[i].err != nil {
			return out[i].err
		}
		if out[i].wroteBefore {
			// Replayed in the transaction, as the serial pass would.
			return nil
		}
		nRows += len(out[i].rows.rows)
		nAcct += len(out[i].acct)
		nStor += len(out[i].stor)
	}
	p.rows.rows = make([]stateRow, 0, nRows)
	p.acct = make([]ChangeSetRow, 0, nAcct)
	if nStor > 0 {
		p.stor = make([]ChangeSetRow, 0, nStor)
	}
	for i := range out {
		p.rows.rows = append(p.rows.rows, out[i].rows.rows...)
		p.acct = append(p.acct, out[i].acct...)
		p.stor = append(p.stor, out[i].stor...)
		p.nAccts += out[i].nAccts
		p.nSlots += out[i].nSlots
	}
	p.fast = true
	if !log.NeedsStore() {
		p.log = nil
	}
	preparedSharded.Add(1)
	return nil
}

// createCheckWriter is the prepared PlainStateWriter: it notes a
// CreateContract of an address whose Storage rows the block already wrote.
type createCheckWriter struct {
	*PlainStateWriter
	rows        *stateRows
	wroteBefore bool
}

func (w *createCheckWriter) CreateContract(address types.Address) error {
	for _, r := range w.rows.rows {
		if r.table == modules.Storage && bytes.HasPrefix(r.key, address[:]) {
			w.wroteBefore = true
		}
	}
	return w.PlainStateWriter.CreateContract(address)
}

// storeHasStorage reports whether tx holds a Storage row of addr.
func storeHasStorage(tx kv.RwTx, addr types.Address) (bool, error) {
	c, err := tx.Cursor(modules.Storage)
	if err != nil {
		return false, err
	}
	defer c.Close()
	k, _, err := c.Seek(addr[:])
	if err != nil {
		return false, err
	}
	return k != nil && bytes.HasPrefix(k, addr[:]), nil
}

// Fast reports whether the state write is applied from prepared rows (after
// WriteState: whether it was).
func (p *PreparedStateWrite) Fast() bool { return p.fast }

// WriteState is CommitBlock's part of the write: the prepared plain rows,
// or the recorded calls replayed on w (the block's writer over tx).
func (p *PreparedStateWrite) WriteState(tx kv.RwTx, w StateWriter) error {
	if p.fast && p.log != nil {
		// Created addresses: the prepared rows assumed the store holds no
		// Storage row of theirs.
		for _, addr := range p.log.creates {
			has, err := storeHasStorage(tx, addr)
			if err != nil {
				return err
			}
			if has {
				p.fast = false
				break
			}
		}
	}
	if p.fast {
		return p.rows.replay(tx)
	}
	return p.log.Replay(w)
}

// WriteChangeSets appends the block's changesets: the prepared rows, or
// w's.
func (p *PreparedStateWrite) WriteChangeSets(tx kv.RwTx, w WriterWithChangeSets) error {
	if p.fast {
		return AppendChangeSetRows(tx, p.acct, p.stor)
	}
	return w.WriteChangeSets()
}

// WriteHistory is w's history write; prepared rows exist only when it
// writes nothing.
func (p *PreparedStateWrite) WriteHistory(w WriterWithChangeSets) error {
	if p.fast {
		return nil
	}
	return w.WriteHistory()
}

// ChangedCounts reports the distinct accounts and slots the block changed
// (w's counts when the log was replayed through it).
func (p *PreparedStateWrite) ChangedCounts(w *PlainStateWriter) (accounts, storage int) {
	if p.fast {
		return p.nAccts, p.nSlots
	}
	return w.ChangedCounts()
}
