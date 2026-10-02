// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// stubWitnessInner is a minimal state.StateReader backing WitnessStateReader
// in these tests. It also implements state.StorageEnumerator so
// ForEachStorage exercises the real forwarding path.
type stubWitnessInner struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash][]byte
	code     map[types.Hash][]byte
}

func (s *stubWitnessInner) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	return s.accounts[addr], nil
}

func (s *stubWitnessInner) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	if m, ok := s.storage[addr]; ok {
		return m[*key], nil
	}
	return nil, nil
}

func (s *stubWitnessInner) ReadAccountCode(addr types.Address, codeHash types.Hash) ([]byte, error) {
	return s.code[codeHash], nil
}

func (s *stubWitnessInner) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	return len(s.code[codeHash]), nil
}

func (s *stubWitnessInner) ForEachStorage(addr types.Address, f func(slot types.Hash, value []byte) bool) error {
	for k, v := range s.storage[addr] {
		if !f(k, v) {
			return nil
		}
	}
	return nil
}

func TestWitnessStateReaderRecordsAndSerializes(t *testing.T) {
	var addr1, addr2 types.Address
	addr1[0] = 0x01
	addr2[0] = 0x02
	slot := types.Hash{0x10}
	codeHash := types.Hash{0x20}
	code := []byte{0x60, 0x01}

	inner := &stubWitnessInner{
		accounts: map[types.Address]*account.StateAccount{
			addr1: {Initialised: true, Nonce: 3},
		},
		storage: map[types.Address]map[types.Hash][]byte{
			addr1: {slot: {0xAA}},
		},
		code: map[types.Hash][]byte{codeHash: code},
	}

	w := NewWitnessStateReader(inner)

	// Empty witness serializes to nil.
	if got := w.Serialize(); got != nil {
		t.Fatalf("Serialize(empty) = %v, want nil", got)
	}
	if w.Len() != 0 {
		t.Fatalf("Len(empty) = %d, want 0", w.Len())
	}

	// ReadAccountData records a present account; addr2 records a miss (nil).
	if _, err := w.ReadAccountData(addr1); err != nil {
		t.Fatalf("ReadAccountData(addr1): %v", err)
	}
	if _, err := w.ReadAccountData(addr2); err != nil {
		t.Fatalf("ReadAccountData(addr2): %v", err)
	}
	// Second read of the same address does not overwrite the recorded entry
	// (first-write-wins); exercised implicitly by calling twice.
	if _, err := w.ReadAccountData(addr1); err != nil {
		t.Fatalf("ReadAccountData(addr1) again: %v", err)
	}

	if _, err := w.ReadAccountStorage(addr1, &slot); err != nil {
		t.Fatalf("ReadAccountStorage: %v", err)
	}
	if _, err := w.ReadAccountCode(addr1, codeHash); err != nil {
		t.Fatalf("ReadAccountCode: %v", err)
	}
	if size, err := w.ReadAccountCodeSize(addr1, codeHash); err != nil || size != len(code) {
		t.Fatalf("ReadAccountCodeSize = %d, %v, want %d, nil", size, err, len(code))
	}

	// ForEachStorage enumerates and records a second slot not touched by an
	// explicit read.
	slot2 := types.Hash{0x11}
	inner.storage[addr1][slot2] = []byte{0xBB}
	visited := map[types.Hash][]byte{}
	if err := w.ForEachStorage(addr1, func(k types.Hash, v []byte) bool {
		visited[k] = v
		return true
	}); err != nil {
		t.Fatalf("ForEachStorage: %v", err)
	}
	if len(visited) != 2 {
		t.Fatalf("ForEachStorage visited %d slots, want 2", len(visited))
	}

	if w.Len() != 2 /* accounts */ +2 /* storage */ +1 /* code */ {
		t.Fatalf("Len() = %d, want 5", w.Len())
	}

	serialized := w.Serialize()
	if len(serialized) == 0 {
		t.Fatal("Serialize() returned empty for a populated witness")
	}
	// Serialization is deterministic across repeated calls (sorted maps).
	again := w.Serialize()
	if string(serialized) != string(again) {
		t.Fatal("Serialize() is not deterministic across calls")
	}

	w.Reset()
	if w.Len() != 0 {
		t.Fatalf("Len() after Reset = %d, want 0", w.Len())
	}
	if got := w.Serialize(); got != nil {
		t.Fatalf("Serialize() after Reset = %v, want nil", got)
	}
}

// TestWitnessStateReaderForEachStorageWithoutEnumerator covers the
// non-enumerator inner fallback (returns nil, nothing recorded).
func TestWitnessStateReaderForEachStorageWithoutEnumerator(t *testing.T) {
	inner := &nonEnumeratingInner{}
	w := NewWitnessStateReader(inner)
	var addr types.Address
	addr[0] = 0x05
	if err := w.ForEachStorage(addr, func(types.Hash, []byte) bool { return true }); err != nil {
		t.Fatalf("ForEachStorage(no enumerator) = %v, want nil", err)
	}
	if w.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 (nothing recorded)", w.Len())
	}
}

type nonEnumeratingInner struct{}

func (nonEnumeratingInner) ReadAccountData(types.Address) (*account.StateAccount, error) {
	return nil, nil
}
func (nonEnumeratingInner) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, nil
}
func (nonEnumeratingInner) ReadAccountCode(types.Address, types.Hash) ([]byte, error) {
	return nil, nil
}
func (nonEnumeratingInner) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, nil
}
