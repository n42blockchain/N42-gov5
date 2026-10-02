// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestBatchWriterPutDeletePendingReset(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	bw := NewBatchWriter(tx, 0) // zero -> default limit
	if bw.limit != 10000 {
		t.Fatalf("default limit = %d, want 10000", bw.limit)
	}

	if err := bw.Put(modules.DatabaseInfo, []byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := bw.Put(modules.DatabaseInfo, []byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := bw.Pending(); got != 2 {
		t.Fatalf("Pending = %d, want 2", got)
	}

	v, err := tx.GetOne(modules.DatabaseInfo, []byte("k1"))
	if err != nil || string(v) != "v1" {
		t.Fatalf("GetOne(k1) = %q, %v", v, err)
	}

	if err := bw.Delete(modules.DatabaseInfo, []byte("k1")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := bw.Pending(); got != 3 {
		t.Fatalf("Pending after delete = %d, want 3", got)
	}
	if v, _ := tx.GetOne(modules.DatabaseInfo, []byte("k1")); v != nil {
		t.Fatal("k1 still present after Delete")
	}

	bw.Reset()
	if got := bw.Pending(); got != 0 {
		t.Fatalf("Pending after Reset = %d, want 0", got)
	}
}

func TestNewBatchWriterCustomLimit(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	bw := NewBatchWriter(tx, 5)
	if bw.limit != 5 {
		t.Fatalf("limit = %d, want 5", bw.limit)
	}
}

func TestKeyBufferGetPut(t *testing.T) {
	kb := NewKeyBuffer(16)

	b := kb.Get()
	if len(b) != 16 {
		t.Fatalf("Get() len = %d, want 16", len(b))
	}
	kb.Put(b)

	b2 := kb.Get()
	if len(b2) != 16 {
		t.Fatalf("Get() (recycled) len = %d, want 16", len(b2))
	}

	// A buffer of the wrong capacity is not pooled (silently dropped).
	kb.Put(make([]byte, 4))

	// Package-level pre-allocated buffers round-trip too.
	hk := HeaderKeyBuffer.Get()
	if len(hk) != 41 {
		t.Fatalf("HeaderKeyBuffer.Get() len = %d, want 41", len(hk))
	}
	HeaderKeyBuffer.Put(hk)

	bbk := BlockBodyKeyBuffer.Get()
	TxLookupKeyBuffer.Put(TxLookupKeyBuffer.Get())
	ReceiptKeyBuffer.Put(ReceiptKeyBuffer.Get())
	BlockNumberKeyBuffer.Put(BlockNumberKeyBuffer.Get())
	BlockBodyKeyBuffer.Put(bbk)
}

func TestValueSizeClass(t *testing.T) {
	cases := []struct {
		size int
		want int
	}{
		{0, 0},
		{16, 0},
		{17, 1},
		{32, 1},
		{33, 2},
	}
	for _, c := range cases {
		if got := valueSizeClass(c.size); got != c.want {
			t.Fatalf("valueSizeClass(%d) = %d, want %d", c.size, got, c.want)
		}
	}

	// A size far larger than any pool class falls back to -1 (direct alloc).
	if got := valueSizeClass(1 << 30); got != -1 {
		t.Fatalf("valueSizeClass(huge) = %d, want -1", got)
	}
}

func TestGetPutValueBuffer(t *testing.T) {
	b := GetValueBuffer(10)
	if len(b) != 10 {
		t.Fatalf("GetValueBuffer(10) len = %d, want 10", len(b))
	}
	PutValueBuffer(b)

	// Round trip through a pooled class again.
	b2 := GetValueBuffer(10)
	if len(b2) != 10 {
		t.Fatalf("GetValueBuffer(10) (recycled) len = %d, want 10", len(b2))
	}
	PutValueBuffer(b2)

	// A size beyond pooling falls back to make([]byte, size) directly and is
	// a no-op on Put (not matching any class' expected size exactly, or
	// matching one incidentally — either way must not panic).
	huge := GetValueBuffer(1 << 21)
	if len(huge) != 1<<21 {
		t.Fatalf("GetValueBuffer(huge) len = %d, want %d", len(huge), 1<<21)
	}
	PutValueBuffer(huge)

	// A buffer whose capacity doesn't exactly match its class's expected
	// size is simply dropped (not pooled), must not panic.
	PutValueBuffer(make([]byte, 0, 100))
}
