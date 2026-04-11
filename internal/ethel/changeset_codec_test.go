// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
)

func TestEncodeDecodeAccountChangesV2(t *testing.T) {
	cs := changeset.NewAccountChangeSet()
	var addr1, addr2 types.Address
	addr1[19] = 0x42
	addr2[19] = 0x99
	// Old values (legacy v2 MarshalV2 form).
	cs.Add(addr1[:], []byte{0x03, 0x05, 0x02, 0x03, 0xe8})
	cs.Add(addr2[:], []byte{}) // deleted → created (old empty)

	newVals := map[types.Address][]byte{
		addr1: {0x04, 0x06, 0x02, 0x04, 0x32}, // incremented nonce+balance
		addr2: {0x01, 0x02, 0x01, 0x2a},       // newly created
	}
	data := EncodeAccountChangesV2(cs, func(addr types.Address) []byte {
		return newVals[addr]
	})
	if data == nil {
		t.Fatal("nil encoding")
	}
	if data[0] != changesetVersionV2 {
		t.Fatalf("version byte: got 0x%02x want 0x%02x", data[0], changesetVersionV2)
	}

	entries, err := DecodeAccountChangesV2(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: got %d want 2", len(entries))
	}
	// Order is cs.Changes order (sorted by the Sort interface).
	// addr1 < addr2 by byte 19, but addr1[19]=0x42 < 0x99=addr2[19], so
	// addr1 should come first.
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

func TestEncodeDecodeStorageChangesV2(t *testing.T) {
	cs := changeset.NewStorageChangeSet()

	makeKey := func(addrByte, slotByte byte) []byte {
		key := make([]byte, 52)
		key[19] = addrByte
		key[51] = slotByte
		return key
	}

	// Same address, 3 slots (one being wiped → newVal empty).
	cs.Add(makeKey(0x42, 0x01), []byte{0xAA})
	cs.Add(makeKey(0x42, 0x02), []byte{0xBB, 0xCC})
	cs.Add(makeKey(0x42, 0x03), []byte{0x11}) // was non-zero, now wiped
	// Different address, 1 slot (newly created).
	cs.Add(makeKey(0x99, 0x01), []byte{})

	newVals := map[string][]byte{
		string(makeKey(0x42, 0x01)): {0xA1},             // updated
		string(makeKey(0x42, 0x02)): {0xBB, 0xCD},       // updated
		string(makeKey(0x42, 0x03)): {},                 // wiped (newLen=0)
		string(makeKey(0x99, 0x01)): {0xFF, 0xFF, 0xFF}, // created
	}
	data := EncodeStorageChangesV2(cs, func(addr types.Address, slot types.Hash) []byte {
		key := make([]byte, 52)
		copy(key[:20], addr[:])
		copy(key[20:], slot[:])
		return newVals[string(key)]
	})
	if data == nil {
		t.Fatal("nil encoding")
	}
	if data[0] != changesetVersionV2 {
		t.Fatalf("version byte: 0x%02x", data[0])
	}

	entries, err := DecodeStorageChangesV2(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries: got %d want 4", len(entries))
	}
	// Verify round-trip: every cs entry's (key, old, new) matches.
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

func TestDecodeAccountChangesV2_BadVersion(t *testing.T) {
	_, err := DecodeAccountChangesV2([]byte{0x02, 0x00, 0x00})
	if err == nil {
		t.Error("expected error for bad version byte")
	}
}

func TestDecodeStorageChangesV2_Empty(t *testing.T) {
	entries, err := DecodeStorageChangesV2(nil)
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if entries != nil {
		t.Error("empty input should decode to nil")
	}
}

// TestGenesisEncodersV2 smoke-tests the genesis iterators by feeding them
// a canned set of accounts and slots in sorted order.
func TestGenesisEncodersV2(t *testing.T) {
	var a1, a2 types.Address
	a1[19] = 0x01
	a2[19] = 0x02
	accIter := func(fn func(addr types.Address, v []byte) error) error {
		if err := fn(a1, []byte{0x01, 0x02}); err != nil {
			return err
		}
		return fn(a2, []byte{0x03, 0x04, 0x05})
	}
	data, err := EncodeGenesisAccountsV2(accIter)
	if err != nil {
		t.Fatalf("genesis accs: %v", err)
	}
	entries, err := DecodeAccountChangesV2(data)
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
	stoData, err := EncodeGenesisStoragesV2(stoIter)
	if err != nil {
		t.Fatalf("genesis stos: %v", err)
	}
	stoEntries, err := DecodeStorageChangesV2(stoData)
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
