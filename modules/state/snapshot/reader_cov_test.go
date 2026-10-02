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
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// TestSnapshotStateReader_StorageHitAndMiss exercises both the layer-hit and
// layer-miss (fall through to inner) branches of ReadAccountStorage.
func TestSnapshotStateReader_StorageHitAndMiss(t *testing.T) {
	tree, genesisRoot := newTestTree()
	accAddr := testAddr(3)
	key := testHash(2)
	val := []byte("stored-value")

	if err := tree.Update(1, testHash(10), genesisRoot, nil, nil,
		map[types.Address]map[types.Hash][]byte{accAddr: {key: val}}); err != nil {
		t.Fatal(err)
	}
	l := tree.Snapshot(testHash(10))

	inner := &mockStateReader{
		storage: map[types.Address]map[types.Hash][]byte{},
	}
	r := NewSnapshotStateReader(l, inner)

	got, err := r.ReadAccountStorage(accAddr, &key)
	if err != nil {
		t.Fatalf("ReadAccountStorage: %v", err)
	}
	if string(got) != string(val) {
		t.Fatalf("got %q want %q", got, val)
	}

	// Miss: a key the layer chain has no record of falls through to inner.
	missAddr := testAddr(44)
	missKey := testHash(99)
	inner.storage[missAddr] = map[types.Hash][]byte{missKey: []byte("from-inner")}
	got2, err := r.ReadAccountStorage(missAddr, &missKey)
	if err != nil {
		t.Fatalf("ReadAccountStorage (miss): %v", err)
	}
	if string(got2) != "from-inner" {
		t.Fatalf("expected fallback to inner reader, got %q", got2)
	}
}

// TestSnapshotStateReader_CodeDelegatesToInner verifies code reads always
// go to the inner reader, regardless of layer state.
func TestSnapshotStateReader_CodeDelegatesToInner(t *testing.T) {
	tree, genesisRoot := newTestTree()
	l := tree.Snapshot(genesisRoot)

	inner := &mockStateReader{}
	r := NewSnapshotStateReader(l, inner)

	// mockStateReader.ReadAccountCode/Size always return zero values; this
	// test exercises the delegation path itself (no error, layer untouched).
	if _, err := r.ReadAccountCode(testAddr(1), testHash(1)); err != nil {
		t.Fatalf("ReadAccountCode: %v", err)
	}
	if _, err := r.ReadAccountCodeSize(testAddr(1), testHash(1)); err != nil {
		t.Fatalf("ReadAccountCodeSize: %v", err)
	}
}

// TestDiffLayer_AccountDeletedFlag covers the single-layer deletion marker,
// which does not consult parent layers.
func TestDiffLayer_AccountDeletedFlag(t *testing.T) {
	tree, genesisRoot := newTestTree()
	addr := testAddr(5)
	if err := tree.Update(1, testHash(11), genesisRoot, nil,
		map[types.Address]struct{}{addr: {}}, nil); err != nil {
		t.Fatal(err)
	}
	dl, ok := tree.Snapshot(testHash(11)).(*DiffLayer)
	if !ok {
		t.Fatalf("expected *DiffLayer")
	}
	if !dl.AccountDeleted(addr) {
		t.Fatalf("expected addr to be marked deleted in this layer")
	}
	if dl.AccountDeleted(testAddr(6)) {
		t.Fatalf("unrelated addr must not be marked deleted")
	}
}

// TestTree_MergeDiffIntoCache_NilCache ensures flattening tolerates a disk
// layer created without a backing ShardedCache.
func TestTree_MergeDiffIntoCache_NilCache(t *testing.T) {
	genesisRoot := testHash(0)
	tree := NewTree(nil, 0, genesisRoot, 1) // maxDiffLayers=1 forces flatten on 2nd update
	addr := testAddr(7)

	if err := tree.Update(1, testHash(21), genesisRoot,
		map[types.Address]*account.StateAccount{addr: testAccount(1)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := tree.Update(2, testHash(22), testHash(21),
		map[types.Address]*account.StateAccount{addr: testAccount(2)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	// No panic means mergeDiffIntoCache's nil-cache guard worked.
}
