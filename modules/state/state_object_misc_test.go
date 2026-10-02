// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func TestCodeAndStorageStringers(t *testing.T) {
	c := Code("hello")
	if c.String() != "hello" {
		t.Fatalf("Code.String = %q, want %q", c.String(), "hello")
	}

	s := Storage{types.Hash{0x01}: *uint256.NewInt(5)}
	if s.String() == "" {
		t.Fatal("Storage.String returned empty string for a non-empty map")
	}

	cpy := s.Copy()
	if len(cpy) != 1 {
		t.Fatalf("Storage.Copy len = %d, want 1", len(cpy))
	}
	cpy[types.Hash{0x02}] = *uint256.NewInt(9)
	if len(s) != 1 {
		t.Fatal("Storage.Copy is not independent of the source map")
	}
}

func TestStateObjectPoolingEnabled(t *testing.T) {
	// Just exercise the accessor; the env-driven value is whatever the test
	// process started with.
	_ = StateObjectPoolingEnabled()
}

func TestStateObjectMiscHelpers(t *testing.T) {
	base := &countingReader{}
	sdb := New(base)

	var addr types.Address
	addr[0] = 0x80
	so := sdb.GetOrNewStateObject(addr)

	if so.Address() != addr {
		t.Fatalf("Address() = %x, want %x", so.Address(), addr)
	}
	if so.Value().Sign() != 0 {
		t.Fatal("Value() should always be zero")
	}
	so.ReturnGas(nil) // no-op, must not panic

	// blockOriginLen/eachBlockOrigin on a freshly created object with no
	// captured block-start values.
	if n := so.blockOriginLen(); n != 0 {
		t.Fatalf("blockOriginLen = %d, want 0", n)
	}
	visited := 0
	so.eachBlockOrigin(func(types.Hash, uint256.Int) { visited++ })
	if visited != 0 {
		t.Fatalf("eachBlockOrigin visited %d entries, want 0", visited)
	}

	// Seed a block-origin value directly (package-internal field) and
	// confirm both accessors see it.
	if so.blockOriginStorage == nil {
		so.blockOriginStorage = map[types.Hash]uint256.Int{}
	}
	key := types.Hash{0x03}
	so.blockOriginStorage[key] = *uint256.NewInt(11)
	if n := so.blockOriginLen(); n != 1 {
		t.Fatalf("blockOriginLen after seeding = %d, want 1", n)
	}
	var seenKey types.Hash
	so.eachBlockOrigin(func(k types.Hash, v uint256.Int) {
		seenKey = k
		if v.Uint64() != 11 {
			t.Fatalf("eachBlockOrigin value = %d, want 11", v.Uint64())
		}
	})
	if seenKey != key {
		t.Fatalf("eachBlockOrigin key = %x, want %x", seenKey, key)
	}

	// setError sticks the first error and ignores later ones.
	so.setError(errors.New("first"))
	so.setError(errors.New("second"))
	if sdb.Error() == nil || sdb.Error().Error() != "first" {
		t.Fatalf("setError = %v, want \"first\"", sdb.Error())
	}

	// touch() must not panic for a non-ripemd address.
	so.touch()

	// printTrie walks dirty keys; must not panic with none set.
	so.printTrie()

	// EncodeRLP produces non-empty bytes for the account payload.
	var buf bytes.Buffer
	if err := so.EncodeRLP(&buf); err != nil {
		t.Fatalf("EncodeRLP: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("EncodeRLP produced no bytes")
	}
}
