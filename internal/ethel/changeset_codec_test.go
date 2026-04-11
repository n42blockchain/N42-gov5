// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
)

func TestEncodeDecodeAccountChanges(t *testing.T) {
	cs := changeset.NewAccountChangeSet()
	var addr1, addr2 types.Address
	addr1[19] = 0x42
	addr2[19] = 0x99
	cs.Add(addr1[:], []byte{0x03, 0x05, 0x02, 0x03, 0xe8})
	cs.Add(addr2[:], []byte{}) // deleted/created (old empty)

	newVals := map[types.Address][]byte{
		addr1: {0x04, 0x06, 0x02, 0x04, 0x32},
		addr2: {0x01, 0x02, 0x01, 0x2a},
	}
	data := EncodeAccountChanges(cs, func(addr types.Address) []byte {
		return newVals[addr]
	})
	if data == nil {
		t.Fatal("nil encoding")
	}

	entries, err := DecodeAccountChanges(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: got %d want 2", len(entries))
	}
	if entries[0].Address != addr1 {
		t.Errorf("entry[0] addr: got %x want %x", entries[0].Address, addr1)
	}
	if !bytes.Equal(entries[0].OldValue, []byte{0x03, 0x05, 0x02, 0x03, 0xe8}) {
		t.Errorf("entry[0] old: %x", entries[0].OldValue)
	}
	if !bytes.Equal(entries[0].NewValue, newVals[addr1]) {
		t.Errorf("entry[0] new: %x want %x", entries[0].NewValue, newVals[addr1])
	}
	if entries[1].Address != addr2 {
		t.Errorf("entry[1] addr: got %x want %x", entries[1].Address, addr2)
	}
	if len(entries[1].OldValue) != 0 {
		t.Errorf("entry[1] old should be empty, got %x", entries[1].OldValue)
	}
	if !bytes.Equal(entries[1].NewValue, newVals[addr2]) {
		t.Errorf("entry[1] new: %x", entries[1].NewValue)
	}
}

func TestEncodeDecodeStorageChanges(t *testing.T) {
	cs := changeset.NewStorageChangeSet()

	makeKey := func(addrByte, slotByte byte) []byte {
		key := make([]byte, 52)
		key[19] = addrByte
		key[51] = slotByte
		return key
	}

	cs.Add(makeKey(0x42, 0x01), []byte{0xAA})
	cs.Add(makeKey(0x42, 0x02), []byte{0xBB, 0xCC})
	cs.Add(makeKey(0x42, 0x03), []byte{0x11})
	cs.Add(makeKey(0x99, 0x01), []byte{})

	newVals := map[string][]byte{
		string(makeKey(0x42, 0x01)): {0xA1},
		string(makeKey(0x42, 0x02)): {0xBB, 0xCD},
		string(makeKey(0x42, 0x03)): {},
		string(makeKey(0x99, 0x01)): {0xFF, 0xFF, 0xFF},
	}
	data := EncodeStorageChanges(cs, func(addr types.Address, slot types.Hash) []byte {
		key := make([]byte, 52)
		copy(key[:20], addr[:])
		copy(key[20:], slot[:])
		return newVals[string(key)]
	})
	if data == nil {
		t.Fatal("nil encoding")
	}

	entries, err := DecodeStorageChanges(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries: got %d want 4", len(entries))
	}
	seen := make(map[string]bool)
	for _, e := range entries {
		seen[string(e.CompositeKey)] = true
		expected := newVals[string(e.CompositeKey)]
		if !bytes.Equal(e.NewValue, expected) {
			t.Errorf("new %x: got %x want %x", e.CompositeKey, e.NewValue, expected)
		}
	}
	for k := range newVals {
		if !seen[k] {
			t.Errorf("missing key %x", []byte(k))
		}
	}
}

func TestDecodeStorageChanges_Empty(t *testing.T) {
	entries, err := DecodeStorageChanges(nil)
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if entries != nil {
		t.Error("empty input should decode to nil")
	}
}

func TestGenesisEncoders(t *testing.T) {
	var a1, a2 types.Address
	a1[19] = 0x01
	a2[19] = 0x02
	accIter := func(fn func(addr types.Address, v []byte) error) error {
		if err := fn(a1, []byte{0x01, 0x02}); err != nil {
			return err
		}
		return fn(a2, []byte{0x03, 0x04, 0x05})
	}
	data, err := EncodeGenesisAccounts(accIter)
	if err != nil {
		t.Fatalf("genesis accs: %v", err)
	}
	entries, err := DecodeAccountChanges(data)
	if err != nil {
		t.Fatalf("decode genesis accs: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("genesis accs: got %d want 2", len(entries))
	}
	for _, e := range entries {
		if len(e.OldValue) != 0 {
			t.Errorf("genesis old should be empty: %x", e.OldValue)
		}
	}
	if !bytes.Equal(entries[0].NewValue, []byte{0x01, 0x02}) ||
		!bytes.Equal(entries[1].NewValue, []byte{0x03, 0x04, 0x05}) {
		t.Errorf("genesis new values wrong")
	}

	stoIter := func(fn func(addr types.Address, slot types.Hash, v []byte) error) error {
		var s1 types.Hash
		s1[31] = 0xAB
		return fn(a1, s1, []byte{0xCC})
	}
	stoData, err := EncodeGenesisStorages(stoIter)
	if err != nil {
		t.Fatalf("genesis stos: %v", err)
	}
	stoEntries, err := DecodeStorageChanges(stoData)
	if err != nil {
		t.Fatalf("decode genesis stos: %v", err)
	}
	if len(stoEntries) != 1 {
		t.Fatalf("genesis stos: got %d want 1", len(stoEntries))
	}
	if len(stoEntries[0].OldValue) != 0 {
		t.Errorf("genesis sto old should be empty")
	}
	if !bytes.Equal(stoEntries[0].NewValue, []byte{0xCC}) {
		t.Errorf("genesis sto new: %x", stoEntries[0].NewValue)
	}
}
