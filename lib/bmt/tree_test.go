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

func newTestTree() *Tree {
	return New(NewMemStore())
}

func randomHash() Hash {
	var h Hash
	if _, err := rand.Read(h[:]); err != nil {
		panic(err)
	}
	return h
}

func TestEmptyTree(t *testing.T) {
	tree := newTestTree()
	root := tree.Root()
	if root != EmptyHash {
		t.Fatalf("empty tree root should be EmptyHash, got %x", root)
	}
}

func TestSingleInsert(t *testing.T) {
	tree := newTestTree()
	key := HashKey([]byte("hello"))
	value := []byte("world")

	if err := tree.Put(key, value); err != nil {
		t.Fatal(err)
	}

	root := tree.Root()
	if root == EmptyHash {
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

	key1 := HashKey([]byte("key1"))
	val1 := []byte("value1")
	key2 := HashKey([]byte("key2"))
	val2 := []byte("value2")

	if err := tree.Put(key1, val1); err != nil {
		t.Fatal(err)
	}
	root1 := tree.Root()

	if err := tree.Put(key2, val2); err != nil {
		t.Fatal(err)
	}
	root2 := tree.Root()

	if root1 == root2 {
		t.Fatal("root should change after second insert")
	}

	got1, err := tree.Get(key1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got1, val1) {
		t.Fatalf("key1: got %q, want %q", got1, val1)
	}

	got2, err := tree.Get(key2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, val2) {
		t.Fatalf("key2: got %q, want %q", got2, val2)
	}
}

func TestInlineLeaf(t *testing.T) {
	tree := newTestTree()
	key := HashKey([]byte("inline"))
	value := []byte("short") // < 32 bytes

	if err := tree.Put(key, value); err != nil {
		t.Fatal(err)
	}

	got, err := tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("got %q, want %q", got, value)
	}

	// Verify value is stored inline (not as hash pointer)
	// The encoded form should be the same length as the value (< 32)
	encoded := encodeLeafValue(value)
	if encoded.IsHashPointer() {
		t.Fatal("short value should be stored inline, not as hash pointer")
	}
}

func TestHashLeaf(t *testing.T) {
	tree := newTestTree()
	key := HashKey([]byte("hashleaf"))

	// Value exactly 32 bytes
	value := make([]byte, 32)
	for i := range value {
		value[i] = byte(i)
	}

	if err := tree.Put(key, value); err != nil {
		t.Fatal(err)
	}

	got, err := tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("got %x, want %x", got, value)
	}

	// Verify encoding uses tag
	encoded := encodeLeafValue(value)
	if len(encoded) != 33 {
		t.Fatalf("32-byte value should encode to 33 bytes (with tag), got %d", len(encoded))
	}
	if encoded[32] != 0x01 {
		t.Fatalf("tag byte should be 0x01, got 0x%02x", encoded[32])
	}
}

func TestDelete(t *testing.T) {
	tree := newTestTree()

	key1 := HashKey([]byte("del1"))
	val1 := []byte("value1")
	key2 := HashKey([]byte("del2"))
	val2 := []byte("value2")

	if err := tree.Put(key1, val1); err != nil {
		t.Fatal(err)
	}
	if err := tree.Put(key2, val2); err != nil {
		t.Fatal(err)
	}

	// Delete key1
	if err := tree.Delete(key1); err != nil {
		t.Fatal(err)
	}

	// key1 should be gone
	_, err := tree.Get(key1)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for deleted key, got %v", err)
	}

	// key2 should still be there
	got2, err := tree.Get(key2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, val2) {
		t.Fatalf("key2: got %q, want %q", got2, val2)
	}

	// Delete key2 — tree should become empty
	if err := tree.Delete(key2); err != nil {
		t.Fatal(err)
	}
	root := tree.Root()
	if root != EmptyHash {
		t.Fatalf("root should be EmptyHash after all deletions, got %x", root)
	}
}

func TestDeleteNonExistent(t *testing.T) {
	tree := newTestTree()
	key := HashKey([]byte("nonexistent"))
	err := tree.Delete(key)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestProofRoundtrip(t *testing.T) {
	tree := newTestTree()

	// Insert several keys to create a multi-level tree
	keys := make([]Hash, 10)
	vals := make([][]byte, 10)
	for i := range keys {
		keys[i] = HashKey([]byte{byte(i), byte(i + 1), byte(i + 2)})
		vals[i] = []byte{byte(i * 10), byte(i*10 + 1)}
	}
	for i, key := range keys {
		if err := tree.Put(key, vals[i]); err != nil {
			t.Fatal(err)
		}
	}

	root := tree.Root()

	// Verify proof for each key
	for i, key := range keys {
		proof, err := tree.GetProof(key)
		if err != nil {
			t.Fatalf("GetProof for key %d: %v", i, err)
		}

		if !bytes.Equal(proof.Value, vals[i]) {
			t.Fatalf("proof value mismatch for key %d", i)
		}

		if !VerifyProof(root, proof) {
			t.Fatalf("proof verification failed for key %d", i)
		}
	}

	// Verify proof fails with wrong root
	wrongRoot := HashKey([]byte("wrong"))
	proof, _ := tree.GetProof(keys[0])
	if VerifyProof(wrongRoot, proof) {
		t.Fatal("proof should fail with wrong root")
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
		if _, err := rand.Read(vals[i]); err != nil {
			t.Fatal(err)
		}
		if err := tree.Put(keys[i], vals[i]); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Verify all keys are retrievable
	for i := 0; i < n; i++ {
		got, err := tree.Get(keys[i])
		if err != nil {
			t.Fatalf("get key %d: %v", i, err)
		}
		if !bytes.Equal(got, vals[i]) {
			t.Fatalf("key %d: got %x, want %x", i, got, vals[i])
		}
	}

	// Root should not be empty
	root := tree.Root()
	if root == EmptyHash {
		t.Fatal("root should not be EmptyHash after 1000 inserts")
	}
}

func TestUpdateValue(t *testing.T) {
	tree := newTestTree()

	key := HashKey([]byte("update-me"))
	val1 := []byte("original")
	val2 := []byte("updated")

	if err := tree.Put(key, val1); err != nil {
		t.Fatal(err)
	}
	root1 := tree.Root()

	got1, err := tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got1, val1) {
		t.Fatalf("got %q, want %q", got1, val1)
	}

	// Update
	if err := tree.Put(key, val2); err != nil {
		t.Fatal(err)
	}
	root2 := tree.Root()

	if root1 == root2 {
		t.Fatal("root should change after value update")
	}

	got2, err := tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, val2) {
		t.Fatalf("got %q, want %q", got2, val2)
	}
}

func TestFlushTo(t *testing.T) {
	store := NewMemStore()
	tree := New(store)

	key1 := HashKey([]byte("flush1"))
	val1 := []byte("data1")
	key2 := HashKey([]byte("flush2"))
	val2 := []byte("data2")

	if err := tree.Put(key1, val1); err != nil {
		t.Fatal(err)
	}
	if err := tree.Put(key2, val2); err != nil {
		t.Fatal(err)
	}

	// Flush to a separate store
	target := NewMemStore()
	if err := tree.FlushTo(target); err != nil {
		t.Fatal(err)
	}

	// Target should have nodes
	if target.Len() == 0 {
		t.Fatal("target store should have nodes after flush")
	}

	// Dirty map should be empty after flush
	if len(tree.dirty) != 0 {
		t.Fatalf("dirty map should be empty after flush, has %d entries", len(tree.dirty))
	}

	// Create a new tree from the target store and verify data
	tree2 := New(target)
	// Copy leafKeys for the new tree to be able to look up values
	for k, v := range tree.leafKeys {
		tree2.leafKeys[k] = v
	}

	got1, err := tree2.Get(key1)
	if err != nil {
		t.Fatalf("get key1 from flushed tree: %v", err)
	}
	if !bytes.Equal(got1, val1) {
		t.Fatalf("key1: got %q, want %q", got1, val1)
	}

	got2, err := tree2.Get(key2)
	if err != nil {
		t.Fatalf("get key2 from flushed tree: %v", err)
	}
	if !bytes.Equal(got2, val2) {
		t.Fatalf("key2: got %q, want %q", got2, val2)
	}
}

func TestPathBit(t *testing.T) {
	// Test bit extraction
	var h Hash
	h[0] = 0xA5 // 10100101
	path := FromKeyHash(h)

	expected := []byte{1, 0, 1, 0, 0, 1, 0, 1}
	for i, exp := range expected {
		got := path.Bit(i)
		if got != exp {
			t.Fatalf("bit %d: got %d, want %d", i, got, exp)
		}
	}
}

func TestPathPrefix(t *testing.T) {
	var h Hash
	h[0] = 0xFF
	path := FromKeyHash(h)

	prefix := path.Prefix(4)
	if prefix.BitLen != 4 {
		t.Fatalf("prefix BitLen: got %d, want 4", prefix.BitLen)
	}
	if prefix.Bytes[0] != 0xF0 {
		t.Fatalf("prefix byte: got %02x, want F0", prefix.Bytes[0])
	}
}

func TestPathSibling(t *testing.T) {
	path := EmptyPath().Append(1).Append(0).Append(1) // 101
	sib := path.Sibling()                              // 100
	if sib.Bit(2) != 0 {
		t.Fatal("sibling should have last bit flipped")
	}
	if sib.Bit(0) != 1 || sib.Bit(1) != 0 {
		t.Fatal("sibling should preserve non-last bits")
	}
}

func TestPathAppendEqual(t *testing.T) {
	p1 := EmptyPath().Append(1).Append(0).Append(1)
	p2 := EmptyPath().Append(1).Append(0).Append(1)
	if !p1.Equal(p2) {
		t.Fatal("equal paths should compare equal")
	}

	p3 := EmptyPath().Append(1).Append(0).Append(0)
	if p1.Equal(p3) {
		t.Fatal("different paths should not compare equal")
	}
}

func TestEncodeMDBXKey(t *testing.T) {
	path := EmptyPath().Append(1).Append(0)
	key := path.EncodeMDBXKey(42)

	// Should be 10 + 1 byte = 11 bytes
	if len(key) != 11 {
		t.Fatalf("key length: got %d, want 11", len(key))
	}

	// Version should be big-endian 42
	if key[7] != 42 {
		t.Fatalf("version byte: got %d, want 42", key[7])
	}

	// BitLen should be 2
	if key[8] != 0 || key[9] != 2 {
		t.Fatalf("bitlen: got [%d, %d], want [0, 2]", key[8], key[9])
	}
}

func TestNodeValueTypes(t *testing.T) {
	// Hash pointer: exactly 32 bytes
	hp := make(NodeValue, 32)
	if !hp.IsHashPointer() {
		t.Fatal("32-byte value should be hash pointer")
	}
	if hp.IsInline() {
		t.Fatal("32-byte value should not be inline")
	}

	// Inline: < 32 bytes
	inline := NodeValue("short")
	if inline.IsHashPointer() {
		t.Fatal("short value should not be hash pointer")
	}
	if !inline.IsInline() {
		t.Fatal("short value should be inline")
	}

	// Tagged: 33 bytes
	tagged := make(NodeValue, 33)
	if tagged.IsHashPointer() {
		t.Fatal("33-byte value should not be hash pointer")
	}
	if !tagged.IsInline() {
		t.Fatal("33-byte value should be inline")
	}

	// Empty
	empty := NodeValue(nil)
	if empty.IsHashPointer() {
		t.Fatal("nil value should not be hash pointer")
	}
	if empty.IsInline() {
		t.Fatal("nil value should not be inline")
	}
}

func TestDeleteAndReinsert(t *testing.T) {
	tree := newTestTree()

	key := HashKey([]byte("reinsert"))
	val1 := []byte("first")
	val2 := []byte("second")

	if err := tree.Put(key, val1); err != nil {
		t.Fatal(err)
	}
	if err := tree.Delete(key); err != nil {
		t.Fatal(err)
	}

	// Re-insert with different value
	if err := tree.Put(key, val2); err != nil {
		t.Fatal(err)
	}

	got, err := tree.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, val2) {
		t.Fatalf("got %q, want %q", got, val2)
	}
}

func TestHashLeafDomainSeparation(t *testing.T) {
	// HashLeaf should produce different results than raw Blake3
	value := []byte("test data")
	leafHash := HashLeaf(value)
	rawHash := HashKey(value)

	if leafHash == rawHash {
		t.Fatal("HashLeaf should use domain separation, producing different hash than raw Blake3")
	}
}

func TestMemStore(t *testing.T) {
	store := NewMemStore()

	path := EmptyPath().Append(1).Append(0)
	val := NodeValue("test-value")

	// Get from empty store
	_, err := store.Get(0, path)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Put and Get
	if err := store.Put(0, path, val); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(0, path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, val) {
		t.Fatalf("got %q, want %q", got, val)
	}

	// Delete
	if err := store.Delete(0, path); err != nil {
		t.Fatal(err)
	}
	_, err = store.Get(0, path)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// Len
	if store.Len() != 0 {
		t.Fatalf("store should be empty, has %d entries", store.Len())
	}
}
