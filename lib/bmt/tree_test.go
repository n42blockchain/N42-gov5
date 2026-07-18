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

package bmt

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func newTestTree() *Tree { return New(NewMemStore()) }

func randomHash() Hash {
	var h Hash
	if _, err := rand.Read(h[:]); err != nil {
		panic(err)
	}
	return h
}

func TestEmptyTree(t *testing.T) {
	tree := newTestTree()
	if tree.Root() != EmptyHash {
		t.Fatalf("empty tree root should be EmptyHash, got %x", tree.Root())
	}
}

func TestSingleInsert(t *testing.T) {
	tree := newTestTree()
	key := HashKey([]byte("hello"))
	value := []byte("world")
	if err := tree.Put(key, value); err != nil {
		t.Fatal(err)
	}
	if tree.Root() == EmptyHash {
		t.Fatal("root should not be EmptyHash after insert")
	}
	got, err := tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("got %q, want %q", got, value)
	}
}

func TestTwoInserts(t *testing.T) {
	tree := newTestTree()
	key1, val1 := HashKey([]byte("key1")), []byte("value1")
	key2, val2 := HashKey([]byte("key2")), []byte("value2")

	tree.Put(key1, val1)
	root1 := tree.Root()
	tree.Put(key2, val2)
	root2 := tree.Root()

	if root1 == root2 {
		t.Fatal("root should change after second insert")
	}
	got1, _ := tree.Get(key1)
	got2, _ := tree.Get(key2)
	if !bytes.Equal(got1, val1) || !bytes.Equal(got2, val2) {
		t.Fatal("values mismatch")
	}
}

func TestDelete(t *testing.T) {
	tree := newTestTree()
	key1, val1 := HashKey([]byte("del1")), []byte("value1")
	key2, val2 := HashKey([]byte("del2")), []byte("value2")

	tree.Put(key1, val1)
	tree.Put(key2, val2)
	tree.Delete(key1)

	if _, err := tree.Get(key1); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	got2, _ := tree.Get(key2)
	if !bytes.Equal(got2, val2) {
		t.Fatal("key2 value mismatch")
	}

	tree.Delete(key2)
	if tree.Root() != EmptyHash {
		t.Fatal("root should be EmptyHash after all deletions")
	}
}

func TestDeleteNonExistent(t *testing.T) {
	tree := newTestTree()
	if err := tree.Delete(HashKey([]byte("nonexistent"))); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateValue(t *testing.T) {
	tree := newTestTree()
	key := HashKey([]byte("update-me"))
	tree.Put(key, []byte("original"))
	root1 := tree.Root()
	tree.Put(key, []byte("updated"))
	root2 := tree.Root()

	if root1 == root2 {
		t.Fatal("root should change after value update")
	}
	got, _ := tree.Get(key)
	if !bytes.Equal(got, []byte("updated")) {
		t.Fatalf("got %q, want %q", got, "updated")
	}
}

func TestDeleteAndReinsert(t *testing.T) {
	tree := newTestTree()
	key := HashKey([]byte("reinsert"))
	tree.Put(key, []byte("first"))
	tree.Delete(key)
	tree.Put(key, []byte("second"))

	got, err := tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("second")) {
		t.Fatalf("got %q, want %q", got, "second")
	}
}

func TestManyInserts(t *testing.T) {
	tree := newTestTree()
	const n = 1000
	keys := make([]Hash, n)
	vals := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = randomHash()
		vals[i] = make([]byte, 10)
		rand.Read(vals[i])
		tree.Put(keys[i], vals[i])
	}
	for i := 0; i < n; i++ {
		got, err := tree.Get(keys[i])
		if err != nil {
			t.Fatalf("get key %d: %v", i, err)
		}
		if !bytes.Equal(got, vals[i]) {
			t.Fatalf("key %d mismatch", i)
		}
	}
	if tree.Root() == EmptyHash {
		t.Fatal("root should not be EmptyHash after 1000 inserts")
	}
}

func TestProofRoundtrip(t *testing.T) {
	tree := newTestTree()
	keys := make([]Hash, 10)
	vals := make([][]byte, 10)
	for i := range keys {
		keys[i] = HashKey([]byte{byte(i), byte(i + 1), byte(i + 2)})
		vals[i] = []byte{byte(i * 10), byte(i*10 + 1)}
		tree.Put(keys[i], vals[i])
	}
	root := tree.Root()

	for i, key := range keys {
		proof, err := tree.GetProof(key)
		if err != nil {
			t.Fatalf("GetProof key %d: %v", i, err)
		}
		if !bytes.Equal(proof.Value, vals[i]) {
			t.Fatalf("proof value mismatch key %d", i)
		}
		if !VerifyProof(root, proof) {
			t.Fatalf("proof verification failed key %d", i)
		}
	}
	// Wrong root should fail
	wrongRoot := HashKey([]byte("wrong"))
	proof, _ := tree.GetProof(keys[0])
	if VerifyProof(wrongRoot, proof) {
		t.Fatal("proof should fail with wrong root")
	}
}

func TestFlushTo(t *testing.T) {
	store := NewMemStore()
	tree := New(store)
	key1, val1 := HashKey([]byte("flush1")), []byte("data1")
	key2, val2 := HashKey([]byte("flush2")), []byte("data2")
	tree.Put(key1, val1)
	tree.Put(key2, val2)

	target := NewMemStore()
	if err := tree.FlushTo(target); err != nil {
		t.Fatal(err)
	}
	if target.Len() == 0 {
		t.Fatal("target store should have nodes after flush")
	}
	if len(tree.dirty) != 0 {
		t.Fatalf("dirty map should be empty, has %d", len(tree.dirty))
	}

	// New tree from flushed store — content-addressed, no leafKeys copy needed
	tree2 := NewFromRoot(target, tree.Root())
	got1, _ := tree2.Get(key1)
	got2, _ := tree2.Get(key2)
	if !bytes.Equal(got1, val1) {
		t.Fatalf("key1: got %q, want %q", got1, val1)
	}
	if !bytes.Equal(got2, val2) {
		t.Fatalf("key2: got %q, want %q", got2, val2)
	}
}

func TestStructuralSharing(t *testing.T) {
	// Verifies content-addressed structural sharing: updating one key
	// reuses unchanged subtree nodes (same hash → same store entry).
	store := NewMemStore()
	tree := New(store)

	key1 := HashKey([]byte("share-A"))
	key2 := HashKey([]byte("share-B"))
	tree.Put(key1, []byte("v1"))
	tree.Put(key2, []byte("v2"))
	tree.FlushTo(store)
	countAfterInsert := store.Len()

	// Update key1 only — key2's subtree should be reused
	tree2 := NewFromRoot(store, tree.Root())
	tree2.Put(key1, []byte("v1-updated"))
	root2 := tree2.Root()
	tree2.FlushTo(store)
	countAfterUpdate := store.Len()

	// Only the path from key1's leaf to root should be new (~depth nodes).
	// key2's leaf and its subtree are reused (same hash).
	newNodes := countAfterUpdate - countAfterInsert
	if newNodes > 30 {
		t.Fatalf("expected ~20 new nodes from structural sharing, got %d", newNodes)
	}

	// Both keys accessible from new root
	tree3 := NewFromRoot(store, root2)
	got1, _ := tree3.Get(key1)
	got2, _ := tree3.Get(key2)
	if !bytes.Equal(got1, []byte("v1-updated")) {
		t.Fatalf("key1: got %q", got1)
	}
	if !bytes.Equal(got2, []byte("v2")) {
		t.Fatalf("key2: got %q", got2)
	}
}

func TestMultipleRootsCoexist(t *testing.T) {
	// Content-addressed trees: old roots still work after new modifications.
	store := NewMemStore()
	tree := New(store)

	key := HashKey([]byte("versioned"))
	tree.Put(key, []byte("block1"))
	root1 := tree.Root()
	tree.FlushTo(store)

	tree.Put(key, []byte("block2"))
	root2 := tree.Root()
	tree.FlushTo(store)

	if root1 == root2 {
		t.Fatal("roots should differ")
	}

	// Old root still works
	old := NewFromRoot(store, root1)
	got, _ := old.Get(key)
	if !bytes.Equal(got, []byte("block1")) {
		t.Fatalf("old root: got %q, want block1", got)
	}

	// New root has updated value
	cur := NewFromRoot(store, root2)
	got, _ = cur.Get(key)
	if !bytes.Equal(got, []byte("block2")) {
		t.Fatalf("new root: got %q, want block2", got)
	}
}

func TestLeafEncoding(t *testing.T) {
	key := HashKey([]byte("test"))
	encoded := encodeLeafValue(key, []byte("val"))
	if !isLeaf(encoded) {
		t.Fatal("should be leaf")
	}
	if isInternal(encoded) {
		t.Fatal("should not be internal")
	}
	got := decodeLeafValue(encoded)
	if !bytes.Equal(got, []byte("val")) {
		t.Fatalf("decoded: got %q, want %q", got, "val")
	}
	h, ok := extractLeafKeyHash(encoded)
	if !ok || h != key {
		t.Fatal("keyHash extraction failed")
	}
}

func TestInternalEncoding(t *testing.T) {
	left := HashKey([]byte("left"))
	right := HashKey([]byte("right"))
	encoded := encodeInternalNode(left, right)
	if !isInternal(encoded) {
		t.Fatal("should be internal")
	}
	if isLeaf(encoded) {
		t.Fatal("should not be leaf")
	}
	gl, gr := decodeInternalNode(encoded)
	if gl != left || gr != right {
		t.Fatal("decode mismatch")
	}
}

func TestPathBit(t *testing.T) {
	var h Hash
	h[0] = 0xA5 // 10100101
	path := FromKeyHash(h)
	expected := []byte{1, 0, 1, 0, 0, 1, 0, 1}
	for i, exp := range expected {
		if path.Bit(i) != exp {
			t.Fatalf("bit %d: got %d, want %d", i, path.Bit(i), exp)
		}
	}
}

func TestMemStore(t *testing.T) {
	store := NewMemStore()
	key := HashKey([]byte("test"))
	val := NodeValue("test-value")

	if _, err := store.Get(key); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	store.Put(key, val)
	got, err := store.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, val) {
		t.Fatalf("got %q, want %q", got, val)
	}
}

// TestInsertOverMissingNodeErrorsNotSilentDiscard is the regression for the
// silent-subtree-loss bug: insert used to treat a getNode failure as an empty
// slot and return just the new leaf, discarding the existing subtree and
// diverging the root with no error (hit when a batch runs over a store lacking
// prior-batch nodes). It must now surface an error instead.
func TestInsertOverMissingNodeErrorsNotSilentDiscard(t *testing.T) {
	// Build a real two-key tree so the root is an internal node.
	src := New(NewMemStore())
	if err := src.Put(HashKey([]byte("alpha")), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := src.Put(HashKey([]byte("beta")), []byte("2")); err != nil {
		t.Fatal(err)
	}
	root := src.Root()
	if root == EmptyHash {
		t.Fatal("expected a non-empty root")
	}

	// Point a new tree at that root but back it with an EMPTY store, so the
	// root node lookup fails.
	orphan := NewFromRoot(NewMemStore(), root)
	if err := orphan.Put(HashKey([]byte("gamma")), []byte("3")); err == nil {
		t.Fatal("Put over a missing root node silently succeeded (subtree discarded)")
	}

	// PutBatch must fail the same way, not rebuild from one leaf.
	orphan2 := NewFromRoot(NewMemStore(), root)
	err := orphan2.PutBatch([]BatchEntry{
		{Key: HashKey([]byte("gamma")), Value: []byte("3")},
		{Key: HashKey([]byte("delta")), Value: []byte("4")},
	})
	if err == nil {
		t.Fatal("PutBatch over a missing root node silently succeeded")
	}
}
