// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"encoding/binary"
	"testing"
)

// TestMVStateViewWriteSet covers the WriteSet accessor that FlushWrites
// consumes internally.
func TestMVStateViewWriteSet(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)
	v := NewMVStateView(mv, base, 5, 0)

	if got := v.WriteSet(); len(got) != 0 {
		t.Fatalf("WriteSet on a fresh view = %v, want empty", got)
	}

	v.Set([]byte("a"), []byte("A"))
	v.Set([]byte("b"), []byte("B"))

	ws := v.WriteSet()
	if len(ws) != 2 || string(ws["a"]) != "A" || string(ws["b"]) != "B" {
		t.Fatalf("WriteSet = %v, want a=A b=B", ws)
	}
}

// TestMapBaseReaderPut covers the test-only write path on MapBaseReader,
// including that it defensively copies the value and later Gets see it.
func TestMapBaseReaderPut(t *testing.T) {
	m := NewMapBaseReader(nil)

	v, err := m.Get([]byte("k"))
	if err != nil || v != nil {
		t.Fatalf("Get before Put = %v, %v, want nil, nil", v, err)
	}

	val := []byte("v1")
	m.Put([]byte("k"), val)
	val[0] = 'X' // mutate caller's slice; stored copy must be unaffected

	got, err := m.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v1" {
		t.Fatalf("Get after Put = %q, want %q (defensive copy)", got, "v1")
	}

	m.Put([]byte("k"), []byte("v2"))
	got, _ = m.Get([]byte("k"))
	if string(got) != "v2" {
		t.Fatalf("Get after overwrite = %q, want %q", got, "v2")
	}
}

// TestMVHashMapLen covers the approximate cross-shard key count.
func TestMVHashMapLen(t *testing.T) {
	m := NewMVHashMap(16)
	if got := m.Len(); got != 0 {
		t.Fatalf("Len on empty map = %d, want 0", got)
	}

	m.Write([]byte("k1"), Version{TxIdx: 1, Incarnation: 0}, []byte("v1"))
	m.Write([]byte("k2"), Version{TxIdx: 2, Incarnation: 0}, []byte("v2"))
	// A second write to an existing key must not increase Len.
	m.Write([]byte("k1"), Version{TxIdx: 3, Incarnation: 0}, []byte("v1b"))

	if got := m.Len(); got != 2 {
		t.Fatalf("Len after writes = %d, want 2", got)
	}
}

// TestEncodeBlockNumKey covers the debug key-building helper.
func TestEncodeBlockNumKey(t *testing.T) {
	got := EncodeBlockNumKey(0x0102030405060708)
	if len(got) != 8 {
		t.Fatalf("EncodeBlockNumKey length = %d, want 8", len(got))
	}
	want := make([]byte, 8)
	binary.BigEndian.PutUint64(want, 0x0102030405060708)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EncodeBlockNumKey = %x, want %x", got, want)
		}
	}
}
