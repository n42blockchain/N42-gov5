// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// journal_record.go — the journal's storage form.
//
// Every state change used to be appended as a journalEntry interface value.
// Converting a multi-word struct to an interface heap-allocates it, so each
// SSTORE, balance change, access-list warm-up and log was one allocation just
// for undo bookkeeping: 2.5 billion objects per 200k dense blocks on the replay
// profile, and at 256 workers those allocations were the largest single
// contributor to contention on the runtime allocator's central lock.
//
// journalRecord is a fixed-size tagged union that holds any entry by value.
// The entry types and their revert methods remain the source of truth: a
// record is unpacked into the original entry and reverted through a direct
// method call on the concrete type, which does not box. Only two rare kinds
// carry a reference (a previous *stateObject on account reset, a previous code
// slice on code change); pointers box without allocating, the code slice is
// the one remaining allocation and happens once per contract creation.

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

type journalKind uint8

const (
	kindCreateObject journalKind = iota + 1
	kindResetObject
	kindSelfdestruct
	kindBalance
	kindBalanceIncrease
	kindBalanceIncreaseTransfer
	kindNonce
	kindStorage
	kindFakeStorage
	kindCode
	kindRefund
	kindAddLog
	kindTouch
	kindAccessListAddAccount
	kindAccessListAddSlot
	kindTransientStorage
	kindStorageWipeAdd
)

// journalRecord holds one change by value. Field reuse by kind:
//
//	addr  account / access-list address
//	key   storage key / slot / log tx hash / previous code hash
//	val   previous balance / previous storage value / balance increase
//	num   previous nonce / previous refund / selfdestruct prev flag
//	ref   *stateObject (reset) / *BalanceIncrease (transfer) / []byte (code)
type journalRecord struct {
	kind    journalKind
	dirties bool
	addr    types.Address
	key     types.Hash
	val     uint256.Int
	num     uint64
	ref     any
}

func (ch createObjectChange) record() journalRecord {
	return journalRecord{kind: kindCreateObject, dirties: true, addr: *ch.account}
}
func (ch resetObjectChange) record() journalRecord {
	return journalRecord{kind: kindResetObject, addr: *ch.account, ref: ch.prev}
}
func (ch selfdestructChange) record() journalRecord {
	var flag uint64
	if ch.prev {
		flag = 1
	}
	return journalRecord{kind: kindSelfdestruct, dirties: true, addr: *ch.account, num: flag, val: ch.prevbalance}
}
func (ch balanceChange) record() journalRecord {
	return journalRecord{kind: kindBalance, dirties: true, addr: *ch.account, val: ch.prev}
}
func (ch balanceIncrease) record() journalRecord {
	return journalRecord{kind: kindBalanceIncrease, dirties: true, addr: *ch.account, val: ch.increase}
}
func (ch balanceIncreaseTransfer) record() journalRecord {
	r := journalRecord{kind: kindBalanceIncreaseTransfer, ref: ch.bi}
	if ch.account != nil {
		r.addr = *ch.account
		r.num = 1 // account present
	}
	return r
}
func (ch nonceChange) record() journalRecord {
	return journalRecord{kind: kindNonce, dirties: true, addr: *ch.account, num: ch.prev}
}
func (ch storageChange) record() journalRecord {
	return journalRecord{kind: kindStorage, dirties: true, addr: *ch.account, key: ch.key, val: ch.prevalue}
}
func (ch fakeStorageChange) record() journalRecord {
	return journalRecord{kind: kindFakeStorage, dirties: true, addr: *ch.account, key: ch.key, val: ch.prevalue}
}
func (ch codeChange) record() journalRecord {
	return journalRecord{kind: kindCode, dirties: true, addr: *ch.account, key: ch.prevhash, ref: ch.prevcode}
}
func (ch refundChange) record() journalRecord {
	return journalRecord{kind: kindRefund, num: ch.prev}
}
func (ch addLogChange) record() journalRecord {
	return journalRecord{kind: kindAddLog, key: ch.txhash}
}
func (ch touchChange) record() journalRecord {
	return journalRecord{kind: kindTouch, dirties: true, addr: *ch.account}
}
func (ch accessListAddAccountChange) record() journalRecord {
	return journalRecord{kind: kindAccessListAddAccount, addr: ch.address}
}
func (ch accessListAddSlotChange) record() journalRecord {
	return journalRecord{kind: kindAccessListAddSlot, addr: ch.address, key: ch.slot}
}
func (ch transientStorageChange) record() journalRecord {
	return journalRecord{kind: kindTransientStorage, addr: *ch.account, key: ch.key, val: ch.prevalue}
}
func (ch storageWipeAddChange) record() journalRecord {
	return journalRecord{kind: kindStorageWipeAdd, addr: *ch.account}
}

// revert unpacks the record into its entry and reverts through the concrete
// type. These are direct calls, not interface calls, so nothing is boxed.
func (r *journalRecord) revert(s *IntraBlockState) {
	switch r.kind {
	case kindCreateObject:
		createObjectChange{account: &r.addr}.revert(s)
	case kindResetObject:
		var prev *stateObject
		if r.ref != nil {
			prev = r.ref.(*stateObject)
		}
		resetObjectChange{account: &r.addr, prev: prev}.revert(s)
	case kindSelfdestruct:
		selfdestructChange{account: &r.addr, prev: r.num == 1, prevbalance: r.val}.revert(s)
	case kindBalance:
		balanceChange{account: &r.addr, prev: r.val}.revert(s)
	case kindBalanceIncrease:
		balanceIncrease{account: &r.addr, increase: r.val}.revert(s)
	case kindBalanceIncreaseTransfer:
		var account *types.Address
		if r.num == 1 {
			account = &r.addr
		}
		balanceIncreaseTransfer{account: account, bi: r.ref.(*BalanceIncrease)}.revert(s)
	case kindNonce:
		nonceChange{account: &r.addr, prev: r.num}.revert(s)
	case kindStorage:
		storageChange{account: &r.addr, key: r.key, prevalue: r.val}.revert(s)
	case kindFakeStorage:
		fakeStorageChange{account: &r.addr, key: r.key, prevalue: r.val}.revert(s)
	case kindCode:
		var prevcode []byte
		if r.ref != nil {
			prevcode = r.ref.([]byte)
		}
		codeChange{account: &r.addr, prevhash: r.key, prevcode: prevcode}.revert(s)
	case kindRefund:
		refundChange{prev: r.num}.revert(s)
	case kindAddLog:
		addLogChange{txhash: r.key}.revert(s)
	case kindTouch:
		touchChange{account: &r.addr}.revert(s)
	case kindAccessListAddAccount:
		accessListAddAccountChange{address: r.addr}.revert(s)
	case kindAccessListAddSlot:
		accessListAddSlotChange{address: r.addr, slot: r.key}.revert(s)
	case kindTransientStorage:
		transientStorageChange{account: &r.addr, key: r.key, prevalue: r.val}.revert(s)
	case kindStorageWipeAdd:
		storageWipeAddChange{account: &r.addr}.revert(s)
	}
}

// entry reconstructs the interface form. Only for callers that inspect
// entries (tests); the hot path never uses it.
func (r *journalRecord) entry() journalEntry {
	switch r.kind {
	case kindCreateObject:
		return createObjectChange{account: &r.addr}
	case kindResetObject:
		var prev *stateObject
		if r.ref != nil {
			prev = r.ref.(*stateObject)
		}
		return resetObjectChange{account: &r.addr, prev: prev}
	case kindSelfdestruct:
		return selfdestructChange{account: &r.addr, prev: r.num == 1, prevbalance: r.val}
	case kindBalance:
		return balanceChange{account: &r.addr, prev: r.val}
	case kindBalanceIncrease:
		return balanceIncrease{account: &r.addr, increase: r.val}
	case kindBalanceIncreaseTransfer:
		var account *types.Address
		if r.num == 1 {
			account = &r.addr
		}
		return balanceIncreaseTransfer{account: account, bi: r.ref.(*BalanceIncrease)}
	case kindNonce:
		return nonceChange{account: &r.addr, prev: r.num}
	case kindStorage:
		return storageChange{account: &r.addr, key: r.key, prevalue: r.val}
	case kindFakeStorage:
		return fakeStorageChange{account: &r.addr, key: r.key, prevalue: r.val}
	case kindCode:
		var prevcode []byte
		if r.ref != nil {
			prevcode = r.ref.([]byte)
		}
		return codeChange{account: &r.addr, prevhash: r.key, prevcode: prevcode}
	case kindRefund:
		return refundChange{prev: r.num}
	case kindAddLog:
		return addLogChange{txhash: r.key}
	case kindTouch:
		return touchChange{account: &r.addr}
	case kindAccessListAddAccount:
		return accessListAddAccountChange{address: r.addr}
	case kindAccessListAddSlot:
		return accessListAddSlotChange{address: r.addr, slot: r.key}
	case kindTransientStorage:
		return transientStorageChange{account: &r.addr, key: r.key, prevalue: r.val}
	case kindStorageWipeAdd:
		return storageWipeAddChange{account: &r.addr}
	}
	return nil
}
