// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshot

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func init() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

func newTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	return memdb.NewTestDB(t)
}

// --- Raw DB accessor tests ---

func TestSnapshotAccountCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0x1111111111111111111111111111111111111111")
	data := []byte{0x01, 0x02, 0x03}

	// Write.
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.WriteSnapshotAccount(tx, addr, data)
	}); err != nil {
		t.Fatalf("WriteSnapshotAccount: %v", err)
	}

	// Read.
	var got []byte
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		got, err = rawdb.ReadSnapshotAccount(tx, addr)
		return err
	}); err != nil {
		t.Fatalf("ReadSnapshotAccount: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("data mismatch: got %x, want %x", got, data)
	}

	// Delete.
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.DeleteSnapshotAccount(tx, addr)
	}); err != nil {
		t.Fatalf("DeleteSnapshotAccount: %v", err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		got, err = rawdb.ReadSnapshotAccount(tx, addr)
		return err
	}); err != nil {
		t.Fatalf("ReadSnapshotAccount after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil after delete, got %x", got)
	}
}

func TestSnapshotDiskRoot(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	root := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	blockNum := uint64(42)

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.WriteSnapshotDiskRoot(tx, root, blockNum)
	}); err != nil {
		t.Fatalf("WriteSnapshotDiskRoot: %v", err)
	}

	var gotRoot types.Hash
	var gotBlock uint64
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		gotRoot, gotBlock, err = rawdb.ReadSnapshotDiskRoot(tx)
		return err
	}); err != nil {
		t.Fatalf("ReadSnapshotDiskRoot: %v", err)
	}
	if gotRoot != root {
		t.Fatalf("root mismatch: got %s, want %s", gotRoot.Hex(), root.Hex())
	}
	if gotBlock != blockNum {
		t.Fatalf("block mismatch: got %d, want %d", gotBlock, blockNum)
	}
}

func TestSnapshotGenComplete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Initially not complete.
	var complete bool
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		complete, err = rawdb.IsSnapshotGenComplete(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("expected not complete initially")
	}

	// Set complete.
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.SetSnapshotGenComplete(tx)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		complete, err = rawdb.IsSnapshotGenComplete(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected complete after setting")
	}
}

func TestSnapshotGenMarker(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	marker := []byte{0xaa, 0xbb}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.WriteSnapshotGenMarker(tx, marker)
	}); err != nil {
		t.Fatal(err)
	}

	var got []byte
	var found bool
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		got, found, err = rawdb.ReadSnapshotGenMarker(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected marker to be found")
	}
	if string(got) != string(marker) {
		t.Fatalf("marker mismatch: got %x, want %x", got, marker)
	}
}

// --- Journal serialization tests ---

func TestJournalSerializeDeserialize(t *testing.T) {
	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")
	key1 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	root := types.HexToHash("0xaaaa")
	parentRoot := types.HexToHash("0xbbbb")

	parentLayer := &DiffLayer{root: parentRoot, block: 99}
	acc := &account.StateAccount{
		Nonce:       5,
		Balance:     *uint256.NewInt(1000),
		Incarnation: 1,
	}

	dl := NewDiffLayer(
		parentLayer,
		100,
		root,
		map[types.Address]*account.StateAccount{addr1: acc},
		map[types.Address]struct{}{addr2: {}},
		map[types.Address]map[types.Hash][]byte{
			addr1: {key1: []byte{0x42}},
		},
	)

	data, err := SerializeDiffLayer(dl)
	if err != nil {
		t.Fatalf("SerializeDiffLayer: %v", err)
	}

	dl2, err := DeserializeDiffLayer(data)
	if err != nil {
		t.Fatalf("DeserializeDiffLayer: %v", err)
	}

	if dl2.block != 100 {
		t.Errorf("block: got %d, want 100", dl2.block)
	}
	if dl2.root != root {
		t.Errorf("root mismatch")
	}
	if len(dl2.accounts) != 1 {
		t.Fatalf("accounts count: got %d, want 1", len(dl2.accounts))
	}
	if dl2.accounts[addr1].Nonce != 5 {
		t.Errorf("account nonce: got %d, want 5", dl2.accounts[addr1].Nonce)
	}
	if len(dl2.accountDels) != 1 {
		t.Fatalf("deletions count: got %d, want 1", len(dl2.accountDels))
	}
	if _, ok := dl2.accountDels[addr2]; !ok {
		t.Error("addr2 not in deletions")
	}
	if len(dl2.storage) != 1 {
		t.Fatalf("storage addrs: got %d, want 1", len(dl2.storage))
	}
	if val := dl2.storage[addr1][key1]; string(val) != string([]byte{0x42}) {
		t.Errorf("storage value mismatch")
	}
}

func TestJournalSaveAndLoad(t *testing.T) {
	db := newTestDB(t)

	root0 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")
	root1 := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	addr := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	tree := NewTree(nil, 0, root0, 128)
	tree.SetDB(db)

	acc := &account.StateAccount{Nonce: 1, Balance: *uint256.NewInt(100)}
	if err := tree.Update(1, root1, root0,
		map[types.Address]*account.StateAccount{addr: acc},
		nil,
		nil,
	); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Save journal.
	if err := tree.SaveJournal(); err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}

	// Verify journal was written.
	ctx := context.Background()
	var entries []rawdb.JournalEntry
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		entries, err = rawdb.ReadAllSnapshotJournal(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 journal entry, got %d", len(entries))
	}
	if entries[0].BlockNum != 1 {
		t.Errorf("journal block: got %d, want 1", entries[0].BlockNum)
	}

	// Create fresh tree and load journal.
	tree2 := NewTree(nil, 0, root0, 128)
	tree2.SetDB(db)
	if err := db.View(ctx, func(tx kv.Tx) error {
		return LoadJournal(tx, tree2)
	}); err != nil {
		t.Fatalf("LoadJournal: %v", err)
	}

	// Verify diff layer was restored.
	layer := tree2.Snapshot(root1)
	if layer == nil {
		t.Fatal("expected layer for root1 after journal load")
	}
	if layer.BlockNumber() != 1 {
		t.Errorf("restored layer block: got %d, want 1", layer.BlockNumber())
	}
	gotAcc, found := layer.Account(addr)
	if !found || gotAcc == nil {
		t.Fatal("account not found in restored layer")
	}
	if gotAcc.Nonce != 1 {
		t.Errorf("restored nonce: got %d, want 1", gotAcc.Nonce)
	}
}

// --- Flatten-to-disk tests ---

func TestTree_FlattenToDisk(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	root0 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")
	addr := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	tree := NewTree(nil, 0, root0, 2) // max 2 diff layers
	tree.SetDB(db)

	acc1 := &account.StateAccount{Nonce: 1, Balance: *uint256.NewInt(100)}
	acc2 := &account.StateAccount{Nonce: 2, Balance: *uint256.NewInt(200)}
	acc3 := &account.StateAccount{Nonce: 3, Balance: *uint256.NewInt(300)}

	root1 := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	root2 := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	root3 := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")

	// Add 3 diff layers — the first should get auto-flattened to disk.
	tree.Update(1, root1, root0, map[types.Address]*account.StateAccount{addr: acc1}, nil, nil)
	tree.Update(2, root2, root1, map[types.Address]*account.StateAccount{addr: acc2}, nil, nil)
	tree.Update(3, root3, root2, map[types.Address]*account.StateAccount{addr: acc3}, nil, nil)

	// Verify account was persisted to flat snapshot table.
	var data []byte
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		data, err = rawdb.ReadSnapshotAccount(tx, addr)
		return err
	}); err != nil {
		t.Fatalf("ReadSnapshotAccount: %v", err)
	}
	if data == nil {
		t.Fatal("expected account to be persisted to SnapshotAccount table")
	}

	// Verify disk root was updated.
	var diskRoot types.Hash
	var diskBlock uint64
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		diskRoot, diskBlock, err = rawdb.ReadSnapshotDiskRoot(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if diskBlock == 0 {
		t.Fatal("disk block should not be 0 after flatten")
	}
	if diskRoot == (types.Hash{}) {
		t.Fatal("disk root should not be zero after flatten")
	}
}

func TestTree_FlattenDeletedAccount(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	root0 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")
	addr := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	tree := NewTree(nil, 0, root0, 1) // flatten after 1 diff
	tree.SetDB(db)

	// Write an account, then delete it.
	root1 := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	acc := &account.StateAccount{Nonce: 1, Balance: *uint256.NewInt(100)}
	tree.Update(1, root1, root0, map[types.Address]*account.StateAccount{addr: acc}, nil, nil)

	root2 := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	tree.Update(2, root2, root1, nil, map[types.Address]struct{}{addr: {}}, nil)

	// Need a 3rd update to trigger flatten of the deletion layer (maxDiffLayers=1).
	root3 := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	tree.Update(3, root3, root2, nil, nil, nil)

	// After flatten, the account should be deleted from the snapshot table.
	var data []byte
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		data, err = rawdb.ReadSnapshotAccount(tx, addr)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Fatal("expected deleted account to not be in snapshot table")
	}
}

// --- DiskLayer read tests ---

func TestDiskLayer_ReadFromDB(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")
	acc := &account.StateAccount{
		Nonce:       10,
		Balance:     *uint256.NewInt(5000),
		Incarnation: 1,
	}

	// Write account to SnapshotAccount table.
	pb := acc.ToProtoMessage()
	enc, _ := proto.Marshal(pb)
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.WriteSnapshotAccount(tx, addr, enc)
	}); err != nil {
		t.Fatal(err)
	}

	root := types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	dl := NewDiskLayer(nil, 100, root)
	dl.SetDB(db)

	// Without genReady, should return (nil, false).
	got, found := dl.Account(addr)
	if found {
		t.Fatal("expected not found when genReady=false")
	}

	// With genReady, should return the account.
	dl.SetGenReady(true)
	got, found = dl.Account(addr)
	if !found {
		t.Fatal("expected found when genReady=true")
	}
	if got.Nonce != 10 {
		t.Errorf("nonce: got %d, want 10", got.Nonce)
	}
	if got.Balance.Uint64() != 5000 {
		t.Errorf("balance: got %d, want 5000", got.Balance.Uint64())
	}
}

// --- Generator test ---

func TestGenerator_RunToCompletion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Write some accounts to the Account table.
	addrs := []types.Address{
		types.HexToAddress("0x1111111111111111111111111111111111111111"),
		types.HexToAddress("0x2222222222222222222222222222222222222222"),
		types.HexToAddress("0x3333333333333333333333333333333333333333"),
	}

	for _, addr := range addrs {
		acc := &account.StateAccount{Nonce: 1, Balance: *uint256.NewInt(100)}
		pb := acc.ToProtoMessage()
		enc, _ := proto.Marshal(pb)
		if err := db.Update(ctx, func(tx kv.RwTx) error {
			return tx.Put(modules.Account, addr.Bytes(), enc)
		}); err != nil {
			t.Fatal(err)
		}
	}

	root := types.HexToHash("0xeeee")
	gen := NewGenerator(db, root, 42)
	gen.Run(ctx)

	// Verify generation is complete.
	var complete bool
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		complete, err = rawdb.IsSnapshotGenComplete(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected generation to be complete")
	}

	// Verify accounts were copied to SnapshotAccount table.
	for _, addr := range addrs {
		var data []byte
		if err := db.View(ctx, func(tx kv.Tx) error {
			var err error
			data, err = rawdb.ReadSnapshotAccount(tx, addr)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if data == nil {
			t.Fatalf("account %s not found in SnapshotAccount", addr.Hex())
		}
	}

	// Verify disk root was written.
	var diskRoot types.Hash
	var diskBlock uint64
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		diskRoot, diskBlock, err = rawdb.ReadSnapshotDiskRoot(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if diskRoot != root {
		t.Errorf("disk root: got %s, want %s", diskRoot.Hex(), root.Hex())
	}
	if diskBlock != 42 {
		t.Errorf("disk block: got %d, want 42", diskBlock)
	}

	if gen.Progress() < 3 {
		t.Errorf("progress: got %d, want >= 3", gen.Progress())
	}
}
