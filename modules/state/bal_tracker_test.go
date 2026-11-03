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
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	accesslist "github.com/n42blockchain/N42/common/bal"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

var balTestRules = &params.Rules{IsSpuriousDragon: true, IsGlamsterdam: true}

func balTestState(t *testing.T, accounts map[types.Address]uint64) *IntraBlockState {
	t.Helper()
	reader := newMVMockReader()
	for addr, balance := range accounts {
		reader.accounts[addr] = &account.StateAccount{Initialised: true, Balance: *uint256.NewInt(balance)}
	}
	ibs := New(reader)
	ibs.EnableBlockAccessList()
	return ibs
}

func balFinalize(t *testing.T, ibs *IntraBlockState) {
	t.Helper()
	if err := ibs.FinalizeTx(balTestRules, NewNoopWriter()); err != nil {
		t.Fatal(err)
	}
}

func balAccountOf(list accesslist.BlockAccessList, addr types.Address) *accesslist.AccountChanges {
	for i := range list {
		if list[i].Address == addr {
			return &list[i]
		}
	}
	return nil
}

func balSlotKey(n uint64) types.Hash {
	var h types.Hash
	uint256.NewInt(n).WriteToSlice(h[:])
	return h
}

// TestBALRecordsChangesPerIndex checks a change is listed under the index
// that made it, with the post value, and only when the value differs from
// the one the index started with.
func TestBALRecordsChangesPerIndex(t *testing.T) {
	a, b := mkAddr(0x0a), mkAddr(0x0b)
	ibs := balTestState(t, map[types.Address]uint64{a: 100})

	ibs.SetBlockAccessIndex(1)
	ibs.SubBalance(a, uint256.NewInt(10))
	ibs.SetNonce(a, 1)
	balFinalize(t, ibs)

	// Index 2 moves the balance away and back: no change, still accessed.
	ibs.SetBlockAccessIndex(2)
	ibs.SubBalance(a, uint256.NewInt(5))
	ibs.AddBalance(a, uint256.NewInt(5))
	_ = ibs.GetBalance(b)
	balFinalize(t, ibs)

	list := ibs.BlockAccessList()
	acc := balAccountOf(list, a)
	if acc == nil {
		t.Fatal("account a missing")
	}
	if len(acc.BalanceChanges) != 1 || acc.BalanceChanges[0].Index != 1 || acc.BalanceChanges[0].PostBalance.Uint64() != 90 {
		t.Fatalf("balance changes %+v, want [{1 90}]", acc.BalanceChanges)
	}
	if len(acc.NonceChanges) != 1 || acc.NonceChanges[0].Index != 1 || acc.NonceChanges[0].PostNonce != 1 {
		t.Fatalf("nonce changes %+v, want [{1 1}]", acc.NonceChanges)
	}
	if accB := balAccountOf(list, b); accB == nil || len(accB.BalanceChanges) != 0 {
		t.Fatalf("read-only account b: %+v", accB)
	}
}

// TestBALRevertedAccessStaysListed checks an access whose writes are reverted
// leaves the address, and the slot as a read, in the list.
func TestBALRevertedAccessStaysListed(t *testing.T) {
	a, c := mkAddr(0x0a), mkAddr(0x0c)
	ibs := balTestState(t, map[types.Address]uint64{a: 100, c: 1})

	ibs.SetBlockAccessIndex(1)
	snap := ibs.Snapshot()
	ibs.SubBalance(a, uint256.NewInt(10))
	ibs.AddBalance(c, uint256.NewInt(10))
	key := balSlotKey(1)
	ibs.SetState(c, &key, *uint256.NewInt(7))
	ibs.RevertToSnapshot(snap)
	balFinalize(t, ibs)

	list := ibs.BlockAccessList()
	accC := balAccountOf(list, c)
	if balAccountOf(list, a) == nil || accC == nil {
		t.Fatalf("reverted accesses dropped: %+v", list)
	}
	if len(accC.BalanceChanges) != 0 || len(accC.StorageChanges) != 0 {
		t.Fatalf("reverted changes recorded: %+v", accC)
	}
	if len(accC.StorageReads) != 1 || accC.StorageReads[0].Uint64() != 1 {
		t.Fatalf("storage reads %+v, want [1]", accC.StorageReads)
	}
}

// TestBALStorageWritesAndReads checks a slot written back to its starting
// value is a read, and a slot written in one index stays a write when a later
// index only reads it.
func TestBALStorageWritesAndReads(t *testing.T) {
	c := mkAddr(0x0c)
	ibs := balTestState(t, map[types.Address]uint64{c: 1})
	one, two := balSlotKey(1), balSlotKey(2)

	ibs.SetBlockAccessIndex(1)
	ibs.SetState(c, &one, *uint256.NewInt(5))
	ibs.SetState(c, &two, *uint256.NewInt(9))
	ibs.SetState(c, &two, *uint256.NewInt(0))
	balFinalize(t, ibs)

	ibs.SetBlockAccessIndex(2)
	var v uint256.Int
	ibs.GetState(c, &one, &v)
	balFinalize(t, ibs)

	acc := balAccountOf(ibs.BlockAccessList(), c)
	if acc == nil || len(acc.StorageChanges) != 1 || acc.StorageChanges[0].Slot.Uint64() != 1 {
		t.Fatalf("storage changes %+v, want slot 1 only", acc)
	}
	if ch := acc.StorageChanges[0].Changes; len(ch) != 1 || ch[0].Index != 1 || ch[0].PostValue.Uint64() != 5 {
		t.Fatalf("slot 1 changes %+v, want [{1 5}]", ch)
	}
	if len(acc.StorageReads) != 1 || acc.StorageReads[0].Uint64() != 2 {
		t.Fatalf("storage reads %+v, want [2]", acc.StorageReads)
	}
}

// TestBALNotedAccountKeepsStartValues checks an address that is only noted
// (a zero withdrawal), and loaded when its index ends, records no change
// against the balance it held at the block start.
func TestBALNotedAccountKeepsStartValues(t *testing.T) {
	a, d := mkAddr(0x0a), mkAddr(0x0d)
	ibs := balTestState(t, map[types.Address]uint64{a: 5_000_000_000})

	ibs.SetBlockAccessIndex(1)
	ibs.RecordAccountAccess(a)
	ibs.AddBalance(d, uint256.NewInt(3)) // credited without being read

	list := ibs.BlockAccessList()
	accA, accD := balAccountOf(list, a), balAccountOf(list, d)
	if accA == nil || len(accA.BalanceChanges) != 0 {
		t.Fatalf("noted account: %+v", accA)
	}
	if accD == nil || len(accD.BalanceChanges) != 1 || accD.BalanceChanges[0].PostBalance.Uint64() != 3 {
		t.Fatalf("blind credit: %+v", accD)
	}
}

// TestBALDisabledByDefault checks a state without EnableBlockAccessList
// records nothing.
func TestBALDisabledByDefault(t *testing.T) {
	ibs := New(newMVMockReader())
	ibs.AddBalance(mkAddr(0x0a), uint256.NewInt(1))
	ibs.SetBlockAccessIndex(1)
	if list := ibs.BlockAccessList(); list != nil {
		t.Fatalf("list %+v, want nil", list)
	}
}

// TestBALDiscardIndexDropsRevertedAccesses checks that the accesses of a
// transaction a builder reverts and leaves out are not listed, and that the
// transaction taking its index is recorded under it.
func TestBALDiscardIndexDropsRevertedAccesses(t *testing.T) {
	a, b, c := mkAddr(0x0a), mkAddr(0x0b), mkAddr(0x0c)
	ibs := balTestState(t, map[types.Address]uint64{a: 100, b: 100})

	ibs.SetBlockAccessIndex(1)
	snap := ibs.Snapshot()
	ibs.SubBalance(b, uint256.NewInt(1))
	var v uint256.Int
	key := balSlotKey(1)
	ibs.GetState(c, &key, &v)
	ibs.RevertToSnapshot(snap)
	ibs.DiscardBlockAccessIndex()

	ibs.SetBlockAccessIndex(1)
	ibs.SubBalance(a, uint256.NewInt(10))
	balFinalize(t, ibs)

	list := ibs.BlockAccessList()
	if len(list) != 1 || list[0].Address != a {
		t.Fatalf("list %+v, want only account a", list)
	}
	if ch := list[0].BalanceChanges; len(ch) != 1 || ch[0].Index != 1 || ch[0].PostBalance.Uint64() != 90 {
		t.Fatalf("balance changes %+v, want [{1 90}]", ch)
	}
}
