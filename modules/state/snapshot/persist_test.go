// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshot

import (
	"context"
	"sync"
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

	// Write account to SnapshotAccount table using V2 encoding.
	enc := make([]byte, acc.EncodingLengthForStorageV2())
	acc.EncodeForStorageV2(enc)
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

// --- Journal edge case tests ---

func TestJournalSerialize_NilDiffLayer(t *testing.T) {
	_, err := SerializeDiffLayer(nil)
	if err == nil {
		t.Fatal("expected error for nil diff layer")
	}
}

func TestJournalDeserialize_TruncatedData(t *testing.T) {
	// Too short — less than minimum header.
	_, err := DeserializeDiffLayer([]byte{0x01, 0x02})
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestJournalDeserialize_CorruptAccountCount(t *testing.T) {
	// Create valid header but corrupt account count.
	data := make([]byte, 76)
	// Block number (8), root (32), parent root (32) = 72
	// Account count = 0xFFFFFFFF (too large) — but passes the 10M check
	data[72] = 0x00
	data[73] = 0x98 // ~10M
	data[74] = 0x96
	data[75] = 0x81 // 10_000_001

	_, err := DeserializeDiffLayer(data)
	if err == nil {
		t.Fatal("expected error for corrupt account count exceeding 10M limit")
	}
}

func TestJournalSerialize_EmptyDiffLayer(t *testing.T) {
	dl := &DiffLayer{
		root:        types.HexToHash("0xaaaa"),
		block:       10,
		accounts:    map[types.Address]*account.StateAccount{},
		accountDels: map[types.Address]struct{}{},
		storage:     map[types.Address]map[types.Hash][]byte{},
	}

	data, err := SerializeDiffLayer(dl)
	if err != nil {
		t.Fatalf("serialize empty layer: %v", err)
	}

	dl2, err := DeserializeDiffLayer(data)
	if err != nil {
		t.Fatalf("deserialize empty layer: %v", err)
	}
	if dl2.block != 10 {
		t.Errorf("block: got %d, want 10", dl2.block)
	}
	if len(dl2.accounts) != 0 {
		t.Errorf("accounts: got %d, want 0", len(dl2.accounts))
	}
}

func TestJournalSerialize_NilAccountValue(t *testing.T) {
	addr := types.HexToAddress("0x1111111111111111111111111111111111111111")
	dl := &DiffLayer{
		root:        types.HexToHash("0xaaaa"),
		block:       10,
		accounts:    map[types.Address]*account.StateAccount{addr: nil},
		accountDels: map[types.Address]struct{}{},
		storage:     map[types.Address]map[types.Hash][]byte{},
	}

	data, err := SerializeDiffLayer(dl)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	dl2, err := DeserializeDiffLayer(data)
	if err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	if dl2.accounts[addr] != nil {
		t.Error("expected nil account value after roundtrip")
	}
}

// --- Journal multi-layer save/load ---

func TestJournalSaveAndLoad_MultipleBlocks(t *testing.T) {
	db := newTestDB(t)

	root0 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")
	root1 := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	root2 := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	root3 := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")

	addr1 := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	addr2 := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	addr3 := types.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")

	tree := NewTree(nil, 0, root0, 128)
	tree.SetDB(db)

	tree.Update(1, root1, root0,
		map[types.Address]*account.StateAccount{addr1: {Nonce: 1, Balance: *uint256.NewInt(10)}},
		nil, nil)
	tree.Update(2, root2, root1,
		map[types.Address]*account.StateAccount{addr2: {Nonce: 2, Balance: *uint256.NewInt(20)}},
		nil, nil)
	tree.Update(3, root3, root2,
		map[types.Address]*account.StateAccount{addr3: {Nonce: 3, Balance: *uint256.NewInt(30)}},
		nil, nil)

	if err := tree.SaveJournal(); err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}

	// Reload into a fresh tree.
	tree2 := NewTree(nil, 0, root0, 128)
	tree2.SetDB(db)
	ctx := context.Background()
	if err := db.View(ctx, func(tx kv.Tx) error {
		return LoadJournal(tx, tree2)
	}); err != nil {
		t.Fatalf("LoadJournal: %v", err)
	}

	// Verify all 3 layers restored.
	for _, r := range []types.Hash{root1, root2, root3} {
		if tree2.Snapshot(r) == nil {
			t.Fatalf("missing layer for root %s after journal load", r.Hex())
		}
	}

	// Verify parent chain is correct.
	l3 := tree2.Snapshot(root3)
	if l3.Parent() == nil {
		t.Fatal("root3 layer should have a parent")
	}
	if l3.Parent().Root() != root2 {
		t.Errorf("root3 parent: got %s, want %s", l3.Parent().Root().Hex(), root2.Hex())
	}

	l2 := tree2.Snapshot(root2)
	if l2.Parent() == nil {
		t.Fatal("root2 layer should have a parent")
	}
	if l2.Parent().Root() != root1 {
		t.Errorf("root2 parent: got %s, want %s", l2.Parent().Root().Hex(), root1.Hex())
	}
}

// --- Storage persistence tests ---

func TestTree_FlattenWithStorage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	root0 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")
	addr := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	key1 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	key2 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002")

	tree := NewTree(nil, 0, root0, 1)
	tree.SetDB(db)

	root1 := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	tree.Update(1, root1, root0,
		map[types.Address]*account.StateAccount{addr: {Nonce: 1, Balance: *uint256.NewInt(100)}},
		nil,
		map[types.Address]map[types.Hash][]byte{
			addr: {key1: []byte{0xaa}, key2: []byte{0xbb}},
		})

	// Add second layer to trigger flatten.
	root2 := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	tree.Update(2, root2, root1, nil, nil, nil)

	// Verify storage was persisted.
	compositeKey1 := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), 0, key1.Bytes())
	compositeKey2 := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), 0, key2.Bytes())

	if err := db.View(ctx, func(tx kv.Tx) error {
		v1, err := rawdb.ReadSnapshotStorage(tx, compositeKey1)
		if err != nil {
			return err
		}
		if v1 == nil || v1[0] != 0xaa {
			t.Errorf("storage key1: got %x, want aa", v1)
		}
		v2, err := rawdb.ReadSnapshotStorage(tx, compositeKey2)
		if err != nil {
			return err
		}
		if v2 == nil || v2[0] != 0xbb {
			t.Errorf("storage key2: got %x, want bb", v2)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// --- DiskLayer storage read test ---

func TestDiskLayer_StorageRead(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	val := []byte{0x42, 0x43}

	compositeKey := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), 0, key.Bytes())
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.WriteSnapshotStorage(tx, compositeKey, val)
	}); err != nil {
		t.Fatal(err)
	}

	root := types.HexToHash("0xdddd")
	dl := NewDiskLayer(nil, 100, root)
	dl.SetDB(db)

	// Without genReady.
	_, found := dl.Storage(addr, key)
	if found {
		t.Fatal("expected not found when genReady=false")
	}

	dl.SetGenReady(true)
	got, found := dl.Storage(addr, key)
	if !found {
		t.Fatal("expected found when genReady=true")
	}
	if len(got) != 2 || got[0] != 0x42 || got[1] != 0x43 {
		t.Errorf("storage value: got %x, want 4243", got)
	}
}

// --- DiskLayer stale guard ---

func TestDiskLayer_AccountNotFoundInDB(t *testing.T) {
	db := newTestDB(t)

	root := types.HexToHash("0xeeee")
	dl := NewDiskLayer(nil, 100, root)
	dl.SetDB(db)
	dl.SetGenReady(true)

	addr := types.HexToAddress("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddead")
	_, found := dl.Account(addr)
	if found {
		t.Fatal("expected not found for non-existent account")
	}
}

// --- Generator context cancellation ---

func TestGenerator_ContextCancelled(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())

	// Write some accounts.
	for i := 0; i < 5; i++ {
		addr := types.HexToAddress("0x" + string([]byte{byte('a' + i)}) + "111111111111111111111111111111111111111")
		acc := &account.StateAccount{Nonce: uint64(i), Balance: *uint256.NewInt(100)}
		pb := acc.ToProtoMessage()
		enc, _ := proto.Marshal(pb)
		if err := db.Update(ctx, func(tx kv.RwTx) error {
			return tx.Put(modules.Account, addr.Bytes(), enc)
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Cancel before running.
	cancel()

	gen := NewGenerator(db, types.HexToHash("0xffff"), 1)
	gen.Run(ctx)

	// Generation should not be complete.
	var complete bool
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		complete, err = rawdb.IsSnapshotGenComplete(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("expected generation to NOT be complete after cancellation")
	}
}

// --- Concurrent tree operations ---

func TestTree_ConcurrentUpdateAndRead(t *testing.T) {
	root0 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")
	tree := NewTree(nil, 0, root0, 128)

	var wg sync.WaitGroup
	const numWriters = 10
	const numReaders = 20

	// Writers add diff layers.
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			parentRoot := root0
			for j := 0; j < 5; j++ {
				blockNum := uint64(idx*100 + j + 1)
				var rootBytes [32]byte
				rootBytes[0] = byte(idx)
				rootBytes[1] = byte(j)
				root := types.BytesToHash(rootBytes[:])
				acc := &account.StateAccount{Nonce: blockNum, Balance: *uint256.NewInt(blockNum)}
				tree.Update(blockNum, root, parentRoot,
					map[types.Address]*account.StateAccount{
						types.HexToAddress("0x1111111111111111111111111111111111111111"): acc,
					}, nil, nil)
				parentRoot = root
			}
		}(i)
	}

	// Readers concurrently read snapshots.
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				tree.Snapshot(root0) // may or may not find layers
				tree.Size()
			}
		}()
	}

	wg.Wait()
	// No panics = pass.
}

// --- ClearSnapshotJournal test ---

func TestJournalClear(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Write a journal entry.
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.WriteSnapshotJournal(tx, 1, []byte{0x01, 0x02})
	}); err != nil {
		t.Fatal(err)
	}

	// Clear.
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.ClearSnapshotJournal(tx)
	}); err != nil {
		t.Fatal(err)
	}

	// Verify empty.
	var entries []rawdb.JournalEntry
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		entries, err = rawdb.ReadAllSnapshotJournal(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after clear, got %d", len(entries))
	}
}

// --- DeleteSnapshotStorageByAddress test ---

func TestSnapshotStorageByAddressDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	key1 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	key2 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002")

	ck1 := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), 0, key1.Bytes())
	ck2 := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), 0, key2.Bytes())

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteSnapshotStorage(tx, ck1, []byte{0x01}); err != nil {
			return err
		}
		return rawdb.WriteSnapshotStorage(tx, ck2, []byte{0x02})
	}); err != nil {
		t.Fatal(err)
	}

	// Delete all storage for address.
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.DeleteSnapshotStorageByAddress(tx, addr)
	}); err != nil {
		t.Fatal(err)
	}

	// Verify both gone.
	if err := db.View(ctx, func(tx kv.Tx) error {
		v1, err := rawdb.ReadSnapshotStorage(tx, ck1)
		if err != nil {
			return err
		}
		if v1 != nil {
			t.Errorf("expected key1 deleted, got %x", v1)
		}
		v2, err := rawdb.ReadSnapshotStorage(tx, ck2)
		if err != nil {
			return err
		}
		if v2 != nil {
			t.Errorf("expected key2 deleted, got %x", v2)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
