// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestG52JournalAppendAndDirty drives the interface-taking append() wrapper
// and the explicit dirty() hack directly against a bare journal, since no
// non-test caller reaches either today.
func TestG52JournalAppendAndDirty(t *testing.T) {
	j := newJournal()
	addr := types.HexToAddress("0x00000000000000000000000000000000000000c1")

	j.append(touchChange{account: &addr})
	if j.length() != 1 {
		t.Fatalf("append did not record an entry: length=%d", j.length())
	}
	if got := j.dirties[addr]; got != 1 {
		t.Fatalf("append-recorded touchChange should dirty addr once, got %d", got)
	}

	j.dirty(addr)
	if got := j.dirties[addr]; got != 2 {
		t.Fatalf("dirty() should bump the counter explicitly: got %d", got)
	}
}

// TestG52JournalAppendPanicsWithoutRecord confirms the documented panic when
// append is handed an entry type with no record() form.
func TestG52JournalAppendPanicsWithoutRecord(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected append to panic for a recordless entry")
		}
	}()
	j := newJournal()
	j.append(noRecordEntry{})
}

type noRecordEntry struct{}

func (noRecordEntry) revert(*IntraBlockState) {}
func (noRecordEntry) dirtied() *types.Address { return nil }

// TestG52JournalRevertEveryEntryKind exercises every journalEntry's revert
// hook by snapshotting, mutating every tracked field once, and reverting;
// each field must come back to its pre-snapshot value. This is the only
// path that drives touchChange.revert, balanceChange.revert, nonceChange.revert,
// codeChange.revert, fakeStorageChange.revert, addLogChange.revert and
// transientStorageChange.revert, none of which any other test reaches.
func TestG52JournalRevertEveryEntryKind(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))
		g52RunJournalRevertEveryEntryKind(t, ibs)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func g52RunJournalRevertEveryEntryKind(t *testing.T, ibs *IntraBlockState) {
	t.Helper()
	addr := types.HexToAddress("0x00000000000000000000000000000000000000c2")
	ibs.CreateAccount(addr, true)
	ibs.AddBalance(addr, uint256.NewInt(1000))
	ibs.SetNonce(addr, 1)
	ibs.SetCode(addr, []byte{0x60, 0x01})
	key := types.HexToHash("0x05")
	ibs.SetState(addr, &key, *uint256.NewInt(7))
	ibs.SetStorage(addr, Storage{key: *uint256.NewInt(7)})
	txHash := types.HexToHash("0xaa")
	ibs.Prepare(txHash, types.Hash{}, 0)
	ibs.AddLog(&block.Log{Address: addr})
	tkey := types.HexToHash("0x06")
	ibs.SetTransientState(addr, tkey, *uint256.NewInt(9))

	preBalance := ibs.GetBalance(addr)
	preNonce := ibs.GetNonce(addr)
	preCode := append([]byte(nil), ibs.GetCode(addr)...)
	preStorage := uint256.Int{}
	ibs.GetState(addr, &key, &preStorage)
	preFake, _ := ibs.fakeStorageValue(addr, key)
	preLogCount := len(ibs.GetLogs(txHash))
	preTransient := ibs.GetTransientState(addr, tkey)

	snap := ibs.Snapshot()

	// touchChange
	ibs.journal.push(touchChange{account: &addr}.record())
	// balanceChange
	ibs.AddBalance(addr, uint256.NewInt(500))
	// nonceChange
	ibs.SetNonce(addr, preNonce+5)
	// codeChange
	ibs.SetCode(addr, []byte{0x60, 0x02, 0x03})
	// fakeStorageChange
	ibs.SetState(addr, &key, *uint256.NewInt(123))
	// addLogChange
	ibs.AddLog(&block.Log{Address: addr})
	// transientStorageChange
	ibs.SetTransientState(addr, tkey, *uint256.NewInt(321))

	ibs.RevertToSnapshot(snap)

	if got := ibs.GetBalance(addr); got.Cmp(preBalance) != 0 {
		t.Fatalf("balanceChange revert mismatch: got %v want %v", got, preBalance)
	}
	if got := ibs.GetNonce(addr); got != preNonce {
		t.Fatalf("nonceChange revert mismatch: got %d want %d", got, preNonce)
	}
	if got := ibs.GetCode(addr); string(got) != string(preCode) {
		t.Fatalf("codeChange revert mismatch: got %x want %x", got, preCode)
	}
	var postFake uint256.Int
	postFake, _ = ibs.fakeStorageValue(addr, key)
	if postFake.Cmp(&preFake) != 0 {
		t.Fatalf("fakeStorageChange revert mismatch: got %v want %v", postFake, preFake)
	}
	if got := len(ibs.GetLogs(txHash)); got != preLogCount {
		t.Fatalf("addLogChange revert mismatch: got %d want %d", got, preLogCount)
	}
	if got := ibs.GetTransientState(addr, tkey); got.Cmp(&preTransient) != 0 {
		t.Fatalf("transientStorageChange revert mismatch: got %v want %v", got, preTransient)
	}
}

// fakeStorageValue is a tiny test-only accessor mirroring stateObject's
// internal fakeStorage map through the public GetState-with-fake path.
func (sdb *IntraBlockState) fakeStorageValue(addr types.Address, key types.Hash) (uint256.Int, bool) {
	obj := sdb.getStateObject(addr)
	if obj == nil || obj.fakeStorage == nil {
		return uint256.Int{}, false
	}
	v, ok := obj.fakeStorage[key]
	return v, ok
}
