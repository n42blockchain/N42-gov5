// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func TestGetPutPooledBalance(t *testing.T) {
	b := GetPooledBalance()
	b.SetUint64(42)
	PutPooledBalance(b)
	if !b.IsZero() {
		t.Fatal("PutPooledBalance did not clear the value")
	}
	PutPooledBalance(nil) // must not panic

	b2 := GetPooledBalance()
	if b2 == nil {
		t.Fatal("GetPooledBalance returned nil")
	}
}

func TestGetPutPooledStorageKey(t *testing.T) {
	k := GetPooledStorageKey()
	k[0] = 0xAB
	PutPooledStorageKey(k)
	if *k != ([32]byte{}) {
		t.Fatal("PutPooledStorageKey did not zero the key")
	}
	PutPooledStorageKey(nil) // must not panic

	k2 := GetPooledStorageKey()
	if k2 == nil {
		t.Fatal("GetPooledStorageKey returned nil")
	}
}

func TestGetPutPooledByteSlice(t *testing.T) {
	b := GetPooledByteSlice(10)
	if len(b) != 10 {
		t.Fatalf("len = %d, want 10", len(b))
	}
	PutPooledByteSlice(b)

	// A request larger than any pooled capacity still returns a usable slice.
	big := GetPooledByteSlice(1 << 20)
	if len(big) != 1<<20 {
		t.Fatalf("len(big) = %d, want %d", len(big), 1<<20)
	}
	// Too large to be pooled; PutPooledByteSlice must be a safe no-op.
	PutPooledByteSlice(big)

	// Too small to be pooled (< 256 cap).
	PutPooledByteSlice(make([]byte, 0, 10))
}

func TestGetPutPooledStorage(t *testing.T) {
	s := GetPooledStorage()
	if s == nil {
		t.Fatal("GetPooledStorage returned nil")
	}
	var key types.Hash
	key[0] = 0x02
	s[key] = *uint256.NewInt(7)
	PutPooledStorage(s)
	if len(s) != 0 {
		t.Fatal("PutPooledStorage did not clear the map")
	}
	PutPooledStorage(nil) // must not panic

	s2 := GetPooledStorage()
	if s2 == nil {
		t.Fatal("GetPooledStorage (recycled) returned nil")
	}
}
