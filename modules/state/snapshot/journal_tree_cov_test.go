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

package snapshot

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// --- SaveJournal / LoadJournal edge cases ---

func TestSaveJournal_NilTree(t *testing.T) {
	db := newTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveJournal(tx, nil)
	}); err != nil {
		t.Fatalf("SaveJournal(nil tree): %v", err)
	}
}

func TestSaveJournal_NoDiffLayers(t *testing.T) {
	db := newTestDB(t)
	tree, _ := newTestTree()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveJournal(tx, tree)
	}); err != nil {
		t.Fatalf("SaveJournal with only a disk layer: %v", err)
	}
}

func TestSaveJournal_SkipsStaleLayers(t *testing.T) {
	db := newTestDB(t)
	tree, genesisRoot := newTestTree()
	addr := testAddr(1)
	root1 := testHash(31)
	if err := tree.Update(1, root1, genesisRoot,
		map[types.Address]*account.StateAccount{addr: testAccount(1)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	dl := tree.Snapshot(root1).(*DiffLayer)
	dl.MarkStale()

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveJournal(tx, tree)
	}); err != nil {
		t.Fatalf("SaveJournal: %v", err)
	}

	var entries []rawdb.JournalEntry
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		entries, err = rawdb.ReadAllSnapshotJournal(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected stale layer to be skipped, got %d entries", len(entries))
	}
}

func TestLoadJournal_CorruptEntrySkipped(t *testing.T) {
	db := newTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteSnapshotJournal(tx, 1, []byte{0x01, 0x02}) // too short to be valid
	}); err != nil {
		t.Fatal(err)
	}

	tree, _ := newTestTree()
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		return LoadJournal(tx, tree)
	}); err != nil {
		t.Fatalf("LoadJournal: %v", err)
	}
}

func TestLoadJournal_ParentFallbackToDisk(t *testing.T) {
	db := newTestDB(t)
	tree, genesisRoot := newTestTree()

	// Build a diff layer whose parent block number (41) will not exist
	// in the target tree, forcing the disk-layer fallback in LoadJournal.
	srcRoot := testHash(50)
	src := NewDiffLayer(nil, 42, srcRoot,
		map[types.Address]*account.StateAccount{testAddr(9): testAccount(1)}, nil, nil)
	data, err := SerializeDiffLayer(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteSnapshotJournal(tx, 42, data)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		return LoadJournal(tx, tree)
	}); err != nil {
		t.Fatalf("LoadJournal: %v", err)
	}

	got := tree.Snapshot(srcRoot)
	if got == nil {
		t.Fatalf("expected loaded layer to be registered")
	}
	gotDl, ok := got.(*DiffLayer)
	if !ok {
		t.Fatalf("expected *DiffLayer")
	}
	if gotDl.Parent() != Layer(tree.diskLayer) {
		t.Fatalf("expected parent to fall back to the disk layer")
	}
	_ = genesisRoot
}

// --- Tree flatten: storage deletion + nil-account defensive branches ---

func TestTree_FlattenStorageDeletion(t *testing.T) {
	db := newTestDB(t)
	cache := layered.NewShardedCache(4, 1024)
	root0 := testHash(0)
	addr := testAddr(2)
	key := testHash(3)

	tree := NewTree(cache, 0, root0, 1) // flatten after 1 diff layer
	tree.SetDB(db)

	root1 := testHash(61)
	// First write a storage value...
	if err := tree.Update(1, root1, root0, nil, nil,
		map[types.Address]map[types.Hash][]byte{addr: {key: []byte("v1")}}); err != nil {
		t.Fatal(err)
	}

	root2 := testHash(62)
	// ...then a later layer records its deletion (nil value).
	if err := tree.Update(2, root2, root1, nil, nil,
		map[types.Address]map[types.Hash][]byte{addr: {key: nil}}); err != nil {
		t.Fatal(err)
	}

	// A third update forces the deletion layer to flatten into disk.
	root3 := testHash(63)
	if err := tree.Update(3, root3, root2, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	var data []byte
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		compositeKey := append(append([]byte{}, addr.Bytes()...), key.Bytes()...)
		data, err = rawdb.ReadSnapshotStorage(tx, compositeKey)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Fatalf("expected storage slot to be deleted from the flat table, got %q", data)
	}
}

func TestTree_MergeDiffIntoCache_NilAccountValue(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	root0 := testHash(0)
	addr := testAddr(8)

	tree := NewTree(cache, 0, root0, 1)

	root1 := testHash(71)
	if err := tree.Update(1, root1, root0,
		map[types.Address]*account.StateAccount{addr: nil}, nil, nil); err != nil {
		t.Fatal(err)
	}
	root2 := testHash(72)
	if err := tree.Update(2, root2, root1, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// No panic/crash means the defensive "acc == nil" skip in
	// mergeDiffIntoCache was exercised without side effects.
}
