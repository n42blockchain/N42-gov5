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
//
// EIP-7928 (Amsterdam) block access list recording on the IntraBlockState.
//
// Every account and storage access of a block access index is noted as it
// happens; when the index ends, each noted account and slot is compared with
// its value at the start of the index and a change is recorded only when the
// value differs. A reverted write therefore leaves the address (or the slot,
// as a read) in the list with no change, and a write back to the starting
// value counts as a read, as the EIP requires.
//
// Recording is off unless EnableBlockAccessList is called, so no other chain
// or fork pays more than a nil check per access.

package state

import (
	"github.com/holiman/uint256"

	accesslist "github.com/n42blockchain/N42/common/bal"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// balAccount is the part of an account the access list tracks.
type balAccount struct {
	balance  uint256.Int
	nonce    uint64
	codeHash types.Hash
}

type balTracker struct {
	builder *accesslist.Builder
	index   uint64
	// muted suppresses recording while the tracker itself reads the state.
	muted bool
	// accounts holds the addresses accessed in the current index.
	accounts map[types.Address]struct{}
	// slots holds, per address, the storage keys accessed in the current
	// index with their values at its start.
	slots map[types.Address]map[types.Hash]uint256.Int
	// last holds each account's values at the end of the last index that
	// accessed it; an account not in it still has its block-start values.
	last map[types.Address]balAccount
}

// EnableBlockAccessList starts recording the EIP-7928 block access list at
// block access index 0, the pre-execution system calls.
func (sdb *IntraBlockState) EnableBlockAccessList() {
	sdb.bal = &balTracker{
		builder:  accesslist.NewBuilder(),
		accounts: make(map[types.Address]struct{}),
		slots:    make(map[types.Address]map[types.Hash]uint256.Int),
		last:     make(map[types.Address]balAccount),
	}
}

// SetBlockAccessIndex ends the current block access index, recording its
// changes, and makes index current. The state must be finalized (FinalizeTx)
// for the index that ends. No-op unless recording is enabled.
func (sdb *IntraBlockState) SetBlockAccessIndex(index uint64) {
	if sdb.bal == nil {
		return
	}
	sdb.closeBlockAccessIndex()
	sdb.bal.index = index
}

// DiscardBlockAccessIndex forgets the accesses noted in the current index,
// keeping the index. A block builder calls it after reverting a transaction
// it leaves out of the block, so the next transaction starts the index
// afresh. No-op unless recording is enabled.
func (sdb *IntraBlockState) DiscardBlockAccessIndex() {
	if t := sdb.bal; t != nil {
		clear(t.accounts)
		clear(t.slots)
	}
}

// BlockAccessList ends the current index and returns the block's access list,
// or nil when recording is not enabled.
func (sdb *IntraBlockState) BlockAccessList() accesslist.BlockAccessList {
	if sdb.bal == nil {
		return nil
	}
	sdb.closeBlockAccessIndex()
	return sdb.bal.builder.Build()
}

// RecordAccountAccess notes an access to addr that touches no state, such as
// a zero-amount withdrawal.
func (sdb *IntraBlockState) RecordAccountAccess(addr types.Address) {
	sdb.balAccountAccess(addr)
}

func (sdb *IntraBlockState) balAccountAccess(addr types.Address) {
	if t := sdb.bal; t != nil && !t.muted {
		t.accounts[addr] = struct{}{}
	}
}

// balSlotAccess notes an access to a storage slot; it must be called before
// the access changes the slot.
func (sdb *IntraBlockState) balSlotAccess(addr types.Address, key *types.Hash) {
	t := sdb.bal
	if t == nil || t.muted {
		return
	}
	t.accounts[addr] = struct{}{}
	slots, ok := t.slots[addr]
	if !ok {
		slots = make(map[types.Hash]uint256.Int)
		t.slots[addr] = slots
	}
	if _, seen := slots[*key]; seen {
		return
	}
	var value uint256.Int
	t.muted = true
	sdb.GetState(addr, key, &value)
	t.muted = false
	slots[*key] = value
}

// balCurrentAccount reads an account's current values; a missing or deleted
// account reads as empty.
func (sdb *IntraBlockState) balCurrentAccount(addr types.Address) (balAccount, *stateObject) {
	so := sdb.getStateObject(addr)
	if so == nil || so.deleted {
		return balAccount{codeHash: crypto.EmptyCodeHash}, nil
	}
	return balAccount{balance: so.data.Balance, nonce: so.data.Nonce, codeHash: balCodeHash(so.data.CodeHash)}, so
}

// balStartAccount returns an account's values at the start of the current
// index.
func (sdb *IntraBlockState) balStartAccount(addr types.Address) balAccount {
	if v, ok := sdb.bal.last[addr]; ok {
		return v
	}
	// Not accessed by an earlier index: the block-start values, which a
	// live object keeps as its original account.
	if so := sdb.stateObjects[addr]; so != nil {
		return balAccount{balance: so.original.Balance, nonce: so.original.Nonce, codeHash: balCodeHash(so.original.CodeHash)}
	}
	return balAccount{codeHash: crypto.EmptyCodeHash}
}

func balCodeHash(h types.Hash) types.Hash {
	if h == (types.Hash{}) {
		return crypto.EmptyCodeHash
	}
	return h
}

func (sdb *IntraBlockState) closeBlockAccessIndex() {
	t := sdb.bal
	t.muted = true
	defer func() { t.muted = false }()

	for _, addr := range sortedAddresses(t.accounts) {
		t.builder.AccountAccess(addr)
		// Read the current values first: that loads an account the index
		// only noted (a zero withdrawal), whose original values are then the
		// block-start ones.
		post, so := sdb.balCurrentAccount(addr)
		pre := sdb.balStartAccount(addr)
		if post.balance != pre.balance {
			t.builder.BalanceChange(t.index, addr, &post.balance)
		}
		if post.nonce != pre.nonce {
			t.builder.NonceChange(t.index, addr, post.nonce)
		}
		if post.codeHash != pre.codeHash {
			var code []byte
			if so != nil {
				code = so.Code()
			}
			t.builder.CodeChange(t.index, addr, code)
		}
		t.last[addr] = post

		for key, start := range t.slots[addr] {
			var value uint256.Int
			if so != nil {
				so.GetState(&key, &value)
			}
			if value != start {
				t.builder.StorageWrite(t.index, addr, key, &value)
			} else {
				t.builder.StorageRead(addr, key)
			}
		}
	}
	clear(t.accounts)
	clear(t.slots)
}
