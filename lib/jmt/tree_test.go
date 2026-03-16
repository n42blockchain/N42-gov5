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

package jmt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"
)

// --- helpers ---

func makeKey(i uint64) Hash {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], i)
	return HashKey(buf[:])
}

func makeValue(i uint64) []byte {
	return []byte(fmt.Sprintf("value_%d", i))
}

func newTestTree() (*Tree, *MemStore) {
	s := NewMemStore()
	t := New(s)
	return t, s
}

// --- Tree basic tests ---

func TestEmptyTree(t *testing.T) {
	tree, _ := newTestTree()
	if tree.Root() != EmptyHash {
		t.Fatal("empty tree root should be zero hash")
	}
	_, err := tree.Get(makeKey(1))
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPutGetSingle(t *testing.T) {
	tree, _ := newTestTree()
	k := makeKey(42)
	v := makeValue(42)

	if err := tree.Put(k, v); err != nil {
		t.Fatal(err)
	}
	if tree.Root() == EmptyHash {
		t.Fatal("root should not be empty after put")
	}

	got, err := tree.Get(k)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, v) {
		t.Fatalf("got %q, want %q", got, v)
	}
}

func TestPutGetMultiple(t *testing.T) {
	tree, _ := newTestTree()
	const n = 100
	for i := uint64(0); i < n; i++ {
		if err := tree.Put(makeKey(i), makeValue(i)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	for i := uint64(0); i < n; i++ {
		got, err := tree.Get(makeKey(i))
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if !bytes.Equal(got, makeValue(i)) {
			t.Fatalf("get %d: got %q, want %q", i, got, makeValue(i))
		}
	}
}

func TestPutOverwrite(t *testing.T) {
	tree, _ := newTestTree()
	k := makeKey(1)

	if err := tree.Put(k, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := tree.Put(k, []byte("new")); err != nil {
		t.Fatal(err)
	}

	got, err := tree.Get(k)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("new")) {
		t.Fatalf("got %q, want %q", got, []byte("new"))
	}
}

func TestDeleteSingle(t *testing.T) {
	tree, _ := newTestTree()
	k := makeKey(1)
	v := makeValue(1)

	if err := tree.Put(k, v); err != nil {
		t.Fatal(err)
	}
	if err := tree.Delete(k); err != nil {
		t.Fatal(err)
	}
	if tree.Root() != EmptyHash {
		t.Fatal("tree should be empty after deleting only key")
	}
	_, err := tree.Get(k)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteNonexistent(t *testing.T) {
	tree, _ := newTestTree()
	if err := tree.Delete(makeKey(1)); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteMultiple(t *testing.T) {
	tree, _ := newTestTree()
	const n = 50
	for i := uint64(0); i < n; i++ {
		tree.Put(makeKey(i), makeValue(i))
	}

	// Delete even keys.
	for i := uint64(0); i < n; i += 2 {
		if err := tree.Delete(makeKey(i)); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}

	// Even keys gone, odd keys remain.
	for i := uint64(0); i < n; i++ {
		_, err := tree.Get(makeKey(i))
		if i%2 == 0 {
			if err != ErrNotFound {
				t.Fatalf("key %d should be deleted", i)
			}
		} else {
			if err != nil {
				t.Fatalf("key %d should exist: %v", i, err)
			}
		}
	}
}

func TestDeleteAll(t *testing.T) {
	tree, _ := newTestTree()
	const n = 30
	for i := uint64(0); i < n; i++ {
		tree.Put(makeKey(i), makeValue(i))
	}
	for i := uint64(0); i < n; i++ {
		if err := tree.Delete(makeKey(i)); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if tree.Root() != EmptyHash {
		t.Fatal("tree should be empty after deleting all keys")
	}
}

// --- Root determinism ---

func TestRootDeterministic(t *testing.T) {
	tree1, _ := newTestTree()
	tree2, _ := newTestTree()

	keys := []uint64{5, 3, 8, 1, 9, 2, 7}
	for _, k := range keys {
		tree1.Put(makeKey(k), makeValue(k))
	}
	// Insert in reverse order.
	for i := len(keys) - 1; i >= 0; i-- {
		tree2.Put(makeKey(keys[i]), makeValue(keys[i]))
	}

	if tree1.Root() != tree2.Root() {
		t.Fatal("trees with same entries should have same root regardless of insertion order")
	}
}

// --- Flush & persistence ---

func TestFlushAndReopen(t *testing.T) {
	s := NewMemStore()
	tree := New(s)

	const n = 20
	for i := uint64(0); i < n; i++ {
		tree.Put(makeKey(i), makeValue(i))
	}
	root := tree.Root()
	if err := tree.Flush(); err != nil {
		t.Fatal(err)
	}

	// Reopen from same store.
	tree2 := NewFromRoot(s, root)
	for i := uint64(0); i < n; i++ {
		got, err := tree2.Get(makeKey(i))
		if err != nil {
			t.Fatalf("reopen get %d: %v", i, err)
		}
		if !bytes.Equal(got, makeValue(i)) {
			t.Fatalf("reopen get %d: wrong value", i)
		}
	}
}

// --- Batch update ---

func TestBatchUpdate(t *testing.T) {
	tree, _ := newTestTree()

	entries := make([]BatchEntry, 100)
	for i := range entries {
		entries[i] = BatchEntry{KeyHash: makeKey(uint64(i)), Value: makeValue(uint64(i))}
	}

	root, err := tree.BatchUpdate(entries)
	if err != nil {
		t.Fatal(err)
	}
	if root == EmptyHash {
		t.Fatal("root should not be empty")
	}

	for i := uint64(0); i < 100; i++ {
		got, err := tree.Get(makeKey(i))
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if !bytes.Equal(got, makeValue(i)) {
			t.Fatalf("get %d: wrong value", i)
		}
	}
}

func TestBatchUpdateWithDeletes(t *testing.T) {
	tree, _ := newTestTree()

	// Insert 50 keys.
	for i := uint64(0); i < 50; i++ {
		tree.Put(makeKey(i), makeValue(i))
	}

	// Batch: delete even, update odd with new values, add 50-99.
	entries := make([]BatchEntry, 0, 100)
	for i := uint64(0); i < 50; i++ {
		if i%2 == 0 {
			entries = append(entries, BatchEntry{KeyHash: makeKey(i), Value: nil})
		} else {
			entries = append(entries, BatchEntry{KeyHash: makeKey(i), Value: []byte("updated")})
		}
	}
	for i := uint64(50); i < 100; i++ {
		entries = append(entries, BatchEntry{KeyHash: makeKey(i), Value: makeValue(i)})
	}

	if _, err := tree.BatchUpdate(entries); err != nil {
		t.Fatal(err)
	}

	for i := uint64(0); i < 100; i++ {
		got, err := tree.Get(makeKey(i))
		if i < 50 && i%2 == 0 {
			if err != ErrNotFound {
				t.Fatalf("key %d should be deleted", i)
			}
		} else if i < 50 {
			if err != nil || !bytes.Equal(got, []byte("updated")) {
				t.Fatalf("key %d should be updated", i)
			}
		} else {
			if err != nil || !bytes.Equal(got, makeValue(i)) {
				t.Fatalf("key %d should exist", i)
			}
		}
	}
}

func TestBatchDeduplicate(t *testing.T) {
	tree, _ := newTestTree()
	k := makeKey(1)
	entries := []BatchEntry{
		{KeyHash: k, Value: []byte("first")},
		{KeyHash: k, Value: []byte("second")},
		{KeyHash: k, Value: []byte("third")},
	}
	if _, err := tree.BatchUpdate(entries); err != nil {
		t.Fatal(err)
	}
	got, err := tree.Get(k)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("third")) {
		t.Fatalf("expected last-writer-wins, got %q", got)
	}
}

// --- Proof tests ---

func TestProofInclusion(t *testing.T) {
	tree, _ := newTestTree()
	const n = 20
	for i := uint64(0); i < n; i++ {
		tree.Put(makeKey(i), makeValue(i))
	}

	for i := uint64(0); i < n; i++ {
		proof, err := tree.GetProof(makeKey(i))
		if err != nil {
			t.Fatalf("proof %d: %v", i, err)
		}
		if proof.Value == nil {
			t.Fatalf("proof %d: expected inclusion", i)
		}
		val, err := VerifyProof(tree.Root(), proof, DefaultHasher())
		if err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
		if !bytes.Equal(val, makeValue(i)) {
			t.Fatalf("verify %d: wrong value", i)
		}
	}
}

func TestProofExclusion(t *testing.T) {
	tree, _ := newTestTree()
	for i := uint64(0); i < 10; i++ {
		tree.Put(makeKey(i), makeValue(i))
	}

	// Key 999 does not exist.
	proof, err := tree.GetProof(makeKey(999))
	if err != nil {
		t.Fatal(err)
	}
	if proof.Value != nil {
		t.Fatal("expected exclusion proof")
	}
	val, err := VerifyProof(tree.Root(), proof, DefaultHasher())
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatal("expected nil value for exclusion")
	}
}

func TestProofEmptyTree(t *testing.T) {
	tree, _ := newTestTree()
	proof, err := tree.GetProof(makeKey(1))
	if err != nil {
		t.Fatal(err)
	}
	val, err := VerifyProof(tree.Root(), proof, DefaultHasher())
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatal("expected nil for empty tree")
	}
}

func TestProofInvalidRoot(t *testing.T) {
	tree, _ := newTestTree()
	tree.Put(makeKey(1), makeValue(1))
	proof, err := tree.GetProof(makeKey(1))
	if err != nil {
		t.Fatal(err)
	}
	fakeRoot := HashKey([]byte("fake"))
	_, err = VerifyProof(fakeRoot, proof, DefaultHasher())
	if err != ErrInvalidProof {
		t.Fatalf("expected ErrInvalidProof, got %v", err)
	}
}

func TestProofRejectsTruncatedPath(t *testing.T) {
	tree, _ := newTestTree()
	for i := uint64(0); i < 20; i++ {
		tree.Put(makeKey(i), makeValue(i))
	}

	proof, err := tree.GetProof(makeKey(7))
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Path) < 2 {
		t.Fatal("expected multi-step proof")
	}

	tampered := *proof
	tampered.Path = append([]ProofEntry(nil), proof.Path[:len(proof.Path)-1]...)

	_, err = VerifyProof(tree.Root(), &tampered, DefaultHasher())
	if err != ErrInvalidProof {
		t.Fatalf("expected ErrInvalidProof, got %v", err)
	}
}

func TestProofRejectsPathForDifferentKey(t *testing.T) {
	tree, _ := newTestTree()
	for i := uint64(0); i < 20; i++ {
		tree.Put(makeKey(i), makeValue(i))
	}

	proof, err := tree.GetProof(makeKey(999))
	if err != nil {
		t.Fatal(err)
	}

	tampered := *proof
	tampered.Key = makeKey(7)
	tampered.Path = append([]ProofEntry(nil), proof.Path...)

	_, err = VerifyProof(tree.Root(), &tampered, DefaultHasher())
	if err != ErrInvalidProof {
		t.Fatalf("expected ErrInvalidProof, got %v", err)
	}
}

// --- Node encode/decode ---

func TestNodeRoundTrip(t *testing.T) {
	h := DefaultHasher()

	// Internal node.
	internal := NewInternalNode()
	internal.Internal.Children[0] = ChildEntry{Hash: makeKey(1), Valid: true}
	internal.Internal.Children[5] = ChildEntry{Hash: makeKey(2), Valid: true}
	internal.Internal.Children[15] = ChildEntry{Hash: makeKey(3), Valid: true}
	checkRoundTrip(t, internal)

	// Leaf node.
	leaf := NewLeafNode(makeKey(42), []byte("hello"), h)
	checkRoundTrip(t, leaf)

	// Extension node.
	ext := NewExtensionNode(NewNibblePathFromSlice([]byte{1, 2, 3, 4}), makeKey(99))
	checkRoundTrip(t, ext)
}

func checkRoundTrip(t *testing.T, original *Node) {
	t.Helper()
	data := EncodeNode(original)
	decoded, err := DecodeNode(data)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	reEncoded := EncodeNode(decoded)
	if !bytes.Equal(data, reEncoded) {
		t.Fatal("round-trip encoding mismatch")
	}
}

// --- NibblePath tests ---

func TestNibblePath(t *testing.T) {
	h := HashKey([]byte("test"))
	np := NewNibblePath(h)
	if np.Len() != 64 {
		t.Fatalf("expected 64 nibbles, got %d", np.Len())
	}
	for i := 0; i < 64; i++ {
		n := np.At(i)
		if n > 15 {
			t.Fatalf("nibble %d out of range: %d", i, n)
		}
	}
}

func TestNibblePathSlice(t *testing.T) {
	np := NewNibblePathFromSlice([]byte{0, 1, 2, 3, 4, 5})
	sub := np.Slice(2, 5)
	if sub.Len() != 3 {
		t.Fatalf("expected length 3, got %d", sub.Len())
	}
	if sub.At(0) != 2 || sub.At(1) != 3 || sub.At(2) != 4 {
		t.Fatal("wrong nibble values in slice")
	}
}

func TestNibblePathCommonPrefix(t *testing.T) {
	a := NewNibblePathFromSlice([]byte{1, 2, 3, 4, 5})
	b := NewNibblePathFromSlice([]byte{1, 2, 3, 7, 8})
	if a.CommonPrefixLen(b) != 3 {
		t.Fatalf("expected common prefix len 3, got %d", a.CommonPrefixLen(b))
	}
}

func TestNibblePathConcat(t *testing.T) {
	a := NewNibblePathFromSlice([]byte{1, 2})
	b := NewNibblePathFromSlice([]byte{3, 4, 5})
	c := a.Concat(b)
	if c.Len() != 5 {
		t.Fatalf("expected length 5, got %d", c.Len())
	}
	expected := []byte{1, 2, 3, 4, 5}
	for i, e := range expected {
		if c.At(i) != e {
			t.Fatalf("nibble %d: got %d, want %d", i, c.At(i), e)
		}
	}
}

// --- Stress test ---

func TestStress1000Keys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}
	tree, _ := newTestTree()
	const n = 1000

	// Insert all.
	for i := uint64(0); i < n; i++ {
		if err := tree.Put(makeKey(i), makeValue(i)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	// Verify all.
	for i := uint64(0); i < n; i++ {
		got, err := tree.Get(makeKey(i))
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if !bytes.Equal(got, makeValue(i)) {
			t.Fatalf("get %d: wrong value", i)
		}
	}

	// Delete random 500.
	rng := rand.New(rand.NewSource(42))
	perm := rng.Perm(n)
	deleted := make(map[uint64]bool)
	for _, idx := range perm[:500] {
		k := uint64(idx)
		if err := tree.Delete(makeKey(k)); err != nil {
			t.Fatalf("delete %d: %v", k, err)
		}
		deleted[k] = true
	}

	// Verify remaining.
	for i := uint64(0); i < n; i++ {
		got, err := tree.Get(makeKey(i))
		if deleted[i] {
			if err != ErrNotFound {
				t.Fatalf("key %d should be deleted", i)
			}
		} else {
			if err != nil || !bytes.Equal(got, makeValue(i)) {
				t.Fatalf("key %d: unexpected result", i)
			}
		}
	}
}

// --- Benchmark ---

func BenchmarkPut(b *testing.B) {
	tree, _ := newTestTree()
	keys := make([]Hash, b.N)
	vals := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		keys[i] = makeKey(uint64(i))
		vals[i] = makeValue(uint64(i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree.Put(keys[i], vals[i])
	}
}

func BenchmarkGet(b *testing.B) {
	tree, _ := newTestTree()
	const n = 10000
	for i := uint64(0); i < n; i++ {
		tree.Put(makeKey(i), makeValue(i))
	}
	keys := make([]Hash, b.N)
	for i := range keys {
		keys[i] = makeKey(uint64(i % n))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree.Get(keys[i])
	}
}

func BenchmarkBatchUpdate1000(b *testing.B) {
	for i := 0; i < b.N; i++ {
		tree, _ := newTestTree()
		entries := make([]BatchEntry, 1000)
		for j := range entries {
			entries[j] = BatchEntry{
				KeyHash: makeKey(uint64(j)),
				Value:   makeValue(uint64(j)),
			}
		}
		tree.BatchUpdate(entries)
	}
}
