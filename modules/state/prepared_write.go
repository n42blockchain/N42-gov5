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
	for _, op := range l.ops {
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
}

// PrepareStateWrite runs ibs.CommitBlock against a StateWriteLog and, when
// no call needs the store and the history index is not written per block,
// produces the plain rows and the encoded changesets of block number. It
// owns ibs while it runs, as the write would.
func PrepareStateWrite(ibs *IntraBlockState, rules *params.Rules, number uint64) (*PreparedStateWrite, error) {
	log := &StateWriteLog{}
	if err := ibs.CommitBlock(rules, log); err != nil {
		return nil, err
	}
	p := &PreparedStateWrite{log: log}
	if number == 0 || !(HistoryIndexDisabled() || HistoryIndexDeferred()) {
		return p, nil
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
