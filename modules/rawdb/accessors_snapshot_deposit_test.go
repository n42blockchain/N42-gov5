// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// --- accessors_deposit.go ---

func TestDepositRoundTrip(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	addr := types.HexToAddress("0x1000000000000000000000000000000000000009")

	if IsDeposit(tx, addr) {
		t.Fatal("IsDeposit = true before any write")
	}
	n, err := DepositNum(tx)
	if err != nil || n != 0 {
		t.Fatalf("DepositNum empty = %d, %v", n, err)
	}

	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	var pub types.PublicKey
	copy(pub[:], sk.PublicKey().Marshal())
	amount := *uint256.NewInt(42)

	if err := PutDeposit(tx, addr, pub, amount); err != nil {
		t.Fatal(err)
	}
	if !IsDeposit(tx, addr) {
		t.Fatal("IsDeposit = false after write")
	}
	n, err = DepositNum(tx)
	if err != nil || n != 1 {
		t.Fatalf("DepositNum after write = %d, %v", n, err)
	}

	gotPub, gotAmount, err := GetDeposit(tx, addr)
	if err != nil {
		t.Fatalf("GetDeposit: %v", err)
	}
	if gotAmount.Uint64() != 42 {
		t.Fatalf("GetDeposit amount = %v, want 42", gotAmount)
	}
	if gotPub != pub {
		t.Fatalf("GetDeposit pubkey mismatch")
	}

	if err := DeleteDeposit(tx, addr); err != nil {
		t.Fatal(err)
	}
	if IsDeposit(tx, addr) {
		t.Fatal("IsDeposit = true after delete")
	}
	if _, _, err := GetDeposit(tx, addr); err == nil {
		t.Fatal("GetDeposit after delete: expected error")
	}
}

func TestGetDepositMalformedData(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	addr := types.HexToAddress("0x2000000000000000000000000000000000000002")

	if err := tx.Put("Deposit", addr[:], []byte("too-short")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GetDeposit(tx, addr); err == nil {
		t.Fatal("expected error for too-short deposit data")
	}
}

// --- accessors_reward.go ---

func TestAccountRewardRoundTrip(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	addr := types.HexToAddress("0x3000000000000000000000000000000000000003")

	got, err := GetAccountReward(tx, addr)
	if err != nil {
		t.Fatalf("GetAccountReward (missing): %v", err)
	}
	if got.Uint64() != 0 {
		t.Fatalf("GetAccountReward (missing) = %v, want 0", got)
	}

	val := uint256.NewInt(777)
	if err := PutAccountReward(tx, addr, val); err != nil {
		t.Fatal(err)
	}
	got, err = GetAccountReward(tx, addr)
	if err != nil || got.Uint64() != 777 {
		t.Fatalf("GetAccountReward = %v, %v", got, err)
	}
}

// --- history_backfill_marker.go ---

func TestHistoryIndexedThroughMarker(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	n, ok, err := ReadHistoryIndexedThrough(tx)
	if err != nil || ok || n != 0 {
		t.Fatalf("missing marker: %d, %v, %v", n, ok, err)
	}

	if err := WriteHistoryIndexedThrough(tx, 12345); err != nil {
		t.Fatal(err)
	}
	n, ok, err = ReadHistoryIndexedThrough(tx)
	if err != nil || !ok || n != 12345 {
		t.Fatalf("after write: %d, %v, %v", n, ok, err)
	}
}

// --- qmdb_undo.go ---

func TestQMDBUndoRoundTrip(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	u1 := &qmdb.BlockUndo{PrevNextSlot: 1}
	u2 := &qmdb.BlockUndo{PrevNextSlot: 2}
	if err := WriteQMDBUndo(tx, 10, u1); err != nil {
		t.Fatal(err)
	}
	if err := WriteQMDBUndo(tx, 11, u2); err != nil {
		t.Fatal(err)
	}

	got, err := ReadQMDBUndos(tx, 9, 11)
	if err != nil {
		t.Fatalf("ReadQMDBUndos: %v", err)
	}
	if len(got) != 2 || got[0].PrevNextSlot != 1 || got[1].PrevNextSlot != 2 {
		t.Fatalf("ReadQMDBUndos = %+v", got)
	}

	// Gap in the window (block 12 missing) returns nil, no error.
	gap, err := ReadQMDBUndos(tx, 9, 20)
	if err != nil || gap != nil {
		t.Fatalf("ReadQMDBUndos with gap = %v, %v", gap, err)
	}

	// target >= head is an error.
	if _, err := ReadQMDBUndos(tx, 11, 10); err == nil {
		t.Fatal("expected error for target >= head")
	}

	if err := PruneQMDBUndoBelow(tx, 11); err != nil {
		t.Fatal(err)
	}
	prunedGap, err := ReadQMDBUndos(tx, 9, 11)
	if err != nil || prunedGap != nil {
		t.Fatalf("ReadQMDBUndos after prune = %v, %v", prunedGap, err)
	}
	stillThere, err := ReadQMDBUndos(tx, 10, 11)
	if err != nil || stillThere == nil {
		t.Fatalf("block 11 pruned unexpectedly: %v, %v", stillThere, err)
	}
}

func TestQMDBAppliedRoundTrip(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	_, _, ok, err := ReadQMDBApplied(tx)
	if err != nil || ok {
		t.Fatalf("missing marker: ok=%v, err=%v", ok, err)
	}

	var hash [32]byte
	hash[0] = 0xab
	if err := WriteQMDBApplied(tx, 55, hash); err != nil {
		t.Fatal(err)
	}
	num, got, ok, err := ReadQMDBApplied(tx)
	if err != nil || !ok || num != 55 || got != hash {
		t.Fatalf("ReadQMDBApplied = %d, %v, %v, %v", num, got, ok, err)
	}
}

// --- accessors_snapshot.go ---

func TestSnapshotAccountStorageCRUD(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	addr := types.HexToAddress("0x4000000000000000000000000000000000000004")

	if got, err := ReadSnapshotAccount(tx, addr); err != nil || got != nil {
		t.Fatalf("ReadSnapshotAccount (missing) = %v, %v", got, err)
	}
	if err := WriteSnapshotAccount(tx, addr, []byte("acct-data")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshotAccount(tx, addr)
	if err != nil || string(got) != "acct-data" {
		t.Fatalf("ReadSnapshotAccount = %q, %v", got, err)
	}
	if err := DeleteSnapshotAccount(tx, addr); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadSnapshotAccount(tx, addr); got != nil {
		t.Fatal("ReadSnapshotAccount after delete not nil")
	}

	key1 := append(append([]byte{}, addr[:]...), make([]byte, 32)...)
	key2 := append(append([]byte{}, addr[:]...), append(make([]byte, 31), 0x01)...)
	other := types.HexToAddress("0x5000000000000000000000000000000000000005")
	keyOther := append(append([]byte{}, other[:]...), make([]byte, 32)...)

	for _, k := range [][]byte{key1, key2, keyOther} {
		if err := WriteSnapshotStorage(tx, k, []byte{0x01}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := ReadSnapshotStorage(tx, key1); err != nil || len(got) != 1 {
		t.Fatalf("ReadSnapshotStorage = %v, %v", got, err)
	}
	if err := DeleteSnapshotStorage(tx, key1); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadSnapshotStorage(tx, key1); got != nil {
		t.Fatal("ReadSnapshotStorage after delete not nil")
	}

	if err := DeleteSnapshotStorageByAddress(tx, addr); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadSnapshotStorage(tx, key2); got != nil {
		t.Fatal("DeleteSnapshotStorageByAddress left a row behind")
	}
	if got, _ := ReadSnapshotStorage(tx, keyOther); got == nil {
		t.Fatal("DeleteSnapshotStorageByAddress deleted an unrelated address's row")
	}
}

func TestSnapshotMetaAndJournal(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	root, num, err := ReadSnapshotDiskRoot(tx)
	if err != nil || root != (types.Hash{}) || num != 0 {
		t.Fatalf("ReadSnapshotDiskRoot (missing) = %v, %d, %v", root, num, err)
	}
	wantRoot := types.Hash{0x09}
	if err := WriteSnapshotDiskRoot(tx, wantRoot, 99); err != nil {
		t.Fatal(err)
	}
	root, num, err = ReadSnapshotDiskRoot(tx)
	if err != nil || root != wantRoot || num != 99 {
		t.Fatalf("ReadSnapshotDiskRoot = %v, %d, %v", root, num, err)
	}

	if _, ok, err := ReadSnapshotGenMarker(tx); err != nil || ok {
		t.Fatalf("ReadSnapshotGenMarker (missing): ok=%v, err=%v", ok, err)
	}
	if err := WriteSnapshotGenMarker(tx, []byte("marker-1")); err != nil {
		t.Fatal(err)
	}
	marker, ok, err := ReadSnapshotGenMarker(tx)
	if err != nil || !ok || string(marker) != "marker-1" {
		t.Fatalf("ReadSnapshotGenMarker = %q, %v, %v", marker, ok, err)
	}

	if complete, err := IsSnapshotGenComplete(tx); err != nil || complete {
		t.Fatalf("IsSnapshotGenComplete (before) = %v, %v", complete, err)
	}
	if err := SetSnapshotGenComplete(tx); err != nil {
		t.Fatal(err)
	}
	if complete, err := IsSnapshotGenComplete(tx); err != nil || !complete {
		t.Fatalf("IsSnapshotGenComplete (after) = %v, %v", complete, err)
	}

	if err := ClearSnapshotGenState(tx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadSnapshotGenMarker(tx); ok {
		t.Fatal("ClearSnapshotGenState left the marker behind")
	}
	if complete, _ := IsSnapshotGenComplete(tx); complete {
		t.Fatal("ClearSnapshotGenState left the complete flag behind")
	}

	// Journal
	if got, err := ReadSnapshotJournalEntry(tx, 1); err != nil || got != nil {
		t.Fatalf("ReadSnapshotJournalEntry (missing) = %v, %v", got, err)
	}
	if err := WriteSnapshotJournal(tx, 1, []byte("diff-1")); err != nil {
		t.Fatal(err)
	}
	if err := WriteSnapshotJournal(tx, 2, []byte("diff-2")); err != nil {
		t.Fatal(err)
	}
	entry, err := ReadSnapshotJournalEntry(tx, 1)
	if err != nil || string(entry) != "diff-1" {
		t.Fatalf("ReadSnapshotJournalEntry = %q, %v", entry, err)
	}
	all, err := ReadAllSnapshotJournal(tx)
	if err != nil || len(all) != 2 {
		t.Fatalf("ReadAllSnapshotJournal = %+v, %v", all, err)
	}
	if err := DeleteSnapshotJournalEntry(tx, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadSnapshotJournalEntry(tx, 1); got != nil {
		t.Fatal("DeleteSnapshotJournalEntry left the row behind")
	}
	if err := ClearSnapshotJournal(tx); err != nil {
		t.Fatal(err)
	}
	all, err = ReadAllSnapshotJournal(tx)
	if err != nil || len(all) != 0 {
		t.Fatalf("ReadAllSnapshotJournal after clear = %+v, %v", all, err)
	}
}
