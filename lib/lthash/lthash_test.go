// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package lthash

import (
	"testing"
)

func TestNew_IsZero(t *testing.T) {
	d := New()
	if !d.IsZero() {
		t.Error("new digest should be zero")
	}
}

func TestAdd_NonZero(t *testing.T) {
	d := New()
	d.Add([]byte("hello"))
	if d.IsZero() {
		t.Error("digest should be non-zero after Add")
	}
}

func TestHomomorphicProperty(t *testing.T) {
	// Add(A) then Add(B) should equal Add(B) then Add(A).
	d1 := New()
	d1.Add([]byte("alice"))
	d1.Add([]byte("bob"))

	d2 := New()
	d2.Add([]byte("bob"))
	d2.Add([]byte("alice"))

	if *d1 != *d2 {
		t.Error("LtHash should be commutative: Add(A)+Add(B) != Add(B)+Add(A)")
	}
}

func TestXORSelfInverse(t *testing.T) {
	d := New()
	original := d.Clone()

	d.Add([]byte("test-element"))
	if d.IsZero() {
		t.Error("should be non-zero after Add")
	}

	d.Remove([]byte("test-element"))
	if *d != *original {
		t.Error("Add then Remove should restore original digest")
	}
}

func TestIncrementalUpdate(t *testing.T) {
	// Update(old, new) should equal Remove(old) + Add(new).
	oldData := []byte("old-account-state")
	newData := []byte("new-account-state")

	d1 := New()
	d1.Add([]byte("other"))
	d1.Add(oldData)
	d1.Update(oldData, newData)

	d2 := New()
	d2.Add([]byte("other"))
	d2.Add(newData)

	if *d1 != *d2 {
		t.Error("Update(old, new) should equal Remove(old) + Add(new)")
	}
}

func TestDeterminism(t *testing.T) {
	d1 := New()
	d1.Add([]byte("deterministic"))

	d2 := New()
	d2.Add([]byte("deterministic"))

	if *d1 != *d2 {
		t.Error("same input should produce same digest")
	}
}

func TestSum_Deterministic(t *testing.T) {
	d := New()
	d.Add([]byte("test"))

	sum1 := d.Sum()
	sum2 := d.Sum()

	if sum1 != sum2 {
		t.Error("Sum should be deterministic")
	}
}

func TestSum_ZeroDigest(t *testing.T) {
	d := New()
	sum := d.Sum()
	// Zero digest should produce a consistent non-zero hash.
	if sum == [32]byte{} {
		t.Error("Sum of zero digest should be a valid BLAKE3 hash, not all zeros")
	}
}

func TestDifferentInputs_DifferentDigests(t *testing.T) {
	d1 := New()
	d1.Add([]byte("input-a"))

	d2 := New()
	d2.Add([]byte("input-b"))

	if *d1 == *d2 {
		t.Error("different inputs should produce different digests")
	}
}

func TestPersistenceRoundtrip(t *testing.T) {
	d := New()
	d.Add([]byte("persist-test"))
	d.Add([]byte("another-element"))

	data, err := d.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}
	if len(data) != DigestSize {
		t.Fatalf("expected %d bytes, got %d", DigestSize, len(data))
	}

	restored := New()
	if err := restored.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary failed: %v", err)
	}

	if *d != *restored {
		t.Error("roundtrip should preserve digest")
	}
}

func TestUnmarshalBinary_TooShort(t *testing.T) {
	d := New()
	if err := d.UnmarshalBinary([]byte{1, 2, 3}); err == nil {
		t.Error("should error on short data")
	}
}

func TestClone_Independent(t *testing.T) {
	d := New()
	d.Add([]byte("original"))

	clone := d.Clone()
	clone.Add([]byte("modified"))

	if *d == *clone {
		t.Error("clone modification should not affect original")
	}
}

func TestMultipleAddRemove(t *testing.T) {
	d := New()
	elements := []string{"alpha", "beta", "gamma", "delta", "epsilon"}

	for _, e := range elements {
		d.Add([]byte(e))
	}
	if d.IsZero() {
		t.Error("should be non-zero after adds")
	}

	for _, e := range elements {
		d.Remove([]byte(e))
	}
	if !d.IsZero() {
		t.Error("should be zero after removing all elements")
	}
}

func TestEncodeElement(t *testing.T) {
	var keyHash [32]byte
	keyHash[0] = 0x42
	value := []byte{0x01, 0x02}

	encoded := EncodeElement('A', keyHash, value)
	if len(encoded) != 1+32+2 {
		t.Fatalf("expected 35 bytes, got %d", len(encoded))
	}
	if encoded[0] != 'A' {
		t.Error("wrong tag")
	}
	if encoded[1] != 0x42 {
		t.Error("wrong key hash byte")
	}
}

func TestLen(t *testing.T) {
	d := New()
	if d.Len() != DigestSize {
		t.Errorf("expected %d, got %d", DigestSize, d.Len())
	}
}

func BenchmarkAdd(b *testing.B) {
	d := New()
	data := []byte("benchmark-account-state-107-bytes-padded-to-typical-size-for-realistic-performance-measurement-1234567890")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Add(data)
	}
}

func BenchmarkUpdate(b *testing.B) {
	d := New()
	oldData := []byte("old-benchmark-account-state-107-bytes-padded-to-typical-size-for-realistic-performance-measurement-12345")
	newData := []byte("new-benchmark-account-state-107-bytes-padded-to-typical-size-for-realistic-performance-measurement-67890")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Update(oldData, newData)
	}
}

func BenchmarkUpdateBatch100(b *testing.B) {
	d := New()
	old := make([][]byte, 100)
	new := make([][]byte, 100)
	for i := range old {
		old[i] = []byte("old-account-" + string(rune(i)))
		new[i] = []byte("new-account-" + string(rune(i)))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 100; j++ {
			d.Update(old[j], new[j])
		}
	}
}

func BenchmarkSum(b *testing.B) {
	d := New()
	d.Add([]byte("test"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.Sum()
	}
}
