// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the pure ChangeSet type, account/storage encode-decode pairs,
// and the Find* lookups that only need a CursorDupSort fake.

package changeset

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/kv"
)

func TestG35ChangeSetBasics(t *testing.T) {
	s := NewChangeSet()
	if s.Len() != 0 {
		t.Fatalf("expected empty changeset")
	}
	if s.KeySize() != 0 {
		t.Fatalf("expected zero key size")
	}

	k1 := []byte{0x02, 0x02}
	k2 := []byte{0x01, 0x01}
	if err := s.Add(k1, []byte("v1")); err != nil {
		t.Fatalf("add k1: %v", err)
	}
	if err := s.Add(k2, []byte("v2")); err != nil {
		t.Fatalf("add k2: %v", err)
	}
	if s.KeySize() != 2 {
		t.Fatalf("expected key size 2, got %d", s.KeySize())
	}

	// wrong-size key must be rejected.
	if err := s.Add([]byte{0x01}, []byte("v")); err == nil {
		t.Fatalf("expected error for mismatched key size")
	}

	// sort.Interface: k2 < k1 lexicographically.
	if !s.Less(1, 0) {
		t.Fatalf("expected index 1 (k2) to sort before index 0 (k1)")
	}
	s.Swap(0, 1)
	if string(s.Changes[0].Key) != string(k2) {
		t.Fatalf("swap did not take effect")
	}

	keys := s.ChangedKeys()
	if _, ok := keys[string(k1)]; !ok {
		t.Fatalf("expected k1 in ChangedKeys")
	}
	if _, ok := keys[string(k2)]; !ok {
		t.Fatalf("expected k2 in ChangedKeys")
	}

	s2 := NewChangeSet()
	_ = s2.Add(k2, []byte("v2"))
	_ = s2.Add(k1, []byte("v1"))
	if !s.Equals(s2) {
		t.Fatalf("expected equal changesets after identical adds")
	}

	str := s.String()
	if str == "" {
		t.Fatalf("expected non-empty String() output")
	}
}

func TestG35ChangeSetLessTieBreaksOnValue(t *testing.T) {
	s := NewChangeSet()
	_ = s.Add([]byte{0x01}, []byte("b"))
	_ = s.Add([]byte{0x01}, []byte("a"))
	if !s.Less(1, 0) {
		t.Fatalf("expected value 'a' to sort before 'b' when keys tie")
	}
}

func TestG35ChangeSetAddEmptyFirstOK(t *testing.T) {
	s := NewChangeSet()
	// First Add with a non-empty key fixes KeySize for subsequent checks even
	// without a preset keyLen.
	if err := s.Add([]byte{0xAA, 0xBB, 0xCC}, []byte("x")); err != nil {
		t.Fatalf("unexpected error on first add: %v", err)
	}
	if err := s.Add([]byte{0x01, 0x02}, []byte("y")); err == nil {
		t.Fatalf("expected key-size mismatch error")
	}
}

func TestG35EncodeDecodeAccounts(t *testing.T) {
	s := NewAccountChangeSet()
	addr := types.Address{1, 2, 3}
	val := []byte("old-balance")
	if err := s.Add(addr.Bytes(), val); err != nil {
		t.Fatalf("add: %v", err)
	}

	var gotK, gotV []byte
	if err := EncodeAccounts(42, s, func(k, v []byte) error {
		gotK, gotV = append([]byte{}, k...), append([]byte{}, v...)
		return nil
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	blockN, k, v, err := DecodeAccounts(gotK, gotV)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if blockN != 42 {
		t.Fatalf("expected blockN 42, got %d", blockN)
	}
	if string(k) != string(addr.Bytes()) {
		t.Fatalf("decoded key mismatch")
	}
	if string(v) != string(val) {
		t.Fatalf("decoded value mismatch")
	}

	// FromDBFormat should dispatch to DecodeAccounts for 8-byte keys.
	blockN2, k2, v2, err := FromDBFormat(gotK, gotV)
	if err != nil {
		t.Fatalf("fromdbformat: %v", err)
	}
	if blockN2 != blockN || string(k2) != string(k) || string(v2) != string(v) {
		t.Fatalf("FromDBFormat mismatch with DecodeAccounts")
	}
}

func TestG35DecodeAccountsShortValue(t *testing.T) {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, 7)
	if _, _, _, err := DecodeAccounts(key, []byte{0x01}); err == nil {
		t.Fatalf("expected error for too-short account value")
	}
}

func TestG35EncodeDecodeStorage(t *testing.T) {
	s := NewStorageChangeSet()
	addr := types.Address{9, 9, 9}
	var slot [32]byte
	slot[31] = 7
	key := append(append([]byte{}, addr.Bytes()...), slot[:]...)
	val := []byte("old-slot-value")
	if err := s.Add(key, val); err != nil {
		t.Fatalf("add: %v", err)
	}

	var gotK, gotV []byte
	if err := EncodeStorage(99, s, func(k, v []byte) error {
		gotK, gotV = append([]byte{}, k...), append([]byte{}, v...)
		return nil
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	blockN, k, v, err := DecodeStorage(gotK, gotV)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if blockN != 99 {
		t.Fatalf("expected blockN 99, got %d", blockN)
	}
	if string(k) != string(key) {
		t.Fatalf("decoded storage key mismatch: got %x want %x", k, key)
	}
	if string(v) != string(val) {
		t.Fatalf("decoded storage value mismatch")
	}

	// FromDBFormat dispatches to DecodeStorage for keys longer than 8 bytes.
	blockN2, k2, v2, err := FromDBFormat(gotK, gotV)
	if err != nil {
		t.Fatalf("fromdbformat: %v", err)
	}
	if blockN2 != blockN || string(k2) != string(k) || string(v2) != string(v) {
		t.Fatalf("FromDBFormat mismatch with DecodeStorage")
	}
}

func TestG35DecodeStorageEmptyValueAndShort(t *testing.T) {
	key := make([]byte, length.BlockNum+length.Addr)
	binary.BigEndian.PutUint64(key, 5)
	// value exactly length.Hash: oldValue becomes nil.
	val := make([]byte, length.Hash)
	blockN, k, v, err := DecodeStorage(key, val)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if blockN != 5 {
		t.Fatalf("expected blockN 5, got %d", blockN)
	}
	if len(k) != length.Addr+length.Hash {
		t.Fatalf("expected 52-byte key, got %d", len(k))
	}
	if v != nil {
		t.Fatalf("expected nil value for exact-length payload, got %v", v)
	}

	// too-short value errors.
	if _, _, _, err := DecodeStorage(key, []byte{0x01}); err == nil {
		t.Fatalf("expected error for too-short storage value")
	}
}

// fakeDupCursor implements kv.CursorDupSort, only SeekBothRange is exercised.
type fakeDupCursor struct {
	kv.CursorDupSort
	key, val []byte
	ret      []byte
	err      error
}

func (f *fakeDupCursor) SeekBothRange(key, value []byte) ([]byte, error) {
	f.key, f.val = key, value
	return f.ret, f.err
}

func TestG35FindAccount(t *testing.T) {
	addr := types.Address{4, 5, 6}
	val := []byte("balance")
	rec := append(append([]byte{}, addr.Bytes()...), val...)
	c := &fakeDupCursor{ret: rec}

	got, err := FindAccount(c, 3, addr.Bytes())
	if err != nil {
		t.Fatalf("FindAccount: %v", err)
	}
	if string(got) != string(val) {
		t.Fatalf("expected %q got %q", val, got)
	}

	// Mismatched prefix -> nil, nil.
	c2 := &fakeDupCursor{ret: append(append([]byte{}, types.Address{1}.Bytes()...), val...)}
	got2, err := FindAccount(c2, 3, addr.Bytes())
	if err != nil {
		t.Fatalf("FindAccount mismatch case: %v", err)
	}
	if got2 != nil {
		t.Fatalf("expected nil for non-matching prefix, got %v", got2)
	}
}

func TestG35FindStorage(t *testing.T) {
	addr := types.Address{7}
	var slot [32]byte
	slot[0] = 0xAB
	key := append(append([]byte{}, addr.Bytes()...), slot[:]...)
	val := []byte("slot-value")
	rec := append(append([]byte{}, slot[:]...), val...)
	c := &fakeDupCursor{ret: rec}

	got, err := FindStorage(c, 11, key)
	if err != nil {
		t.Fatalf("FindStorage: %v", err)
	}
	if string(got) != string(val) {
		t.Fatalf("expected %q got %q", val, got)
	}

	// Non-matching slot prefix -> ErrNotFound.
	var otherSlot [32]byte
	otherSlot[0] = 0xCD
	c2 := &fakeDupCursor{ret: append(append([]byte{}, otherSlot[:]...), val...)}
	if _, err := FindStorage(c2, 11, key); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestG35Mapper(t *testing.T) {
	// Sanity-check both Mapper entries are wired to the right encode/decode pair.
	acc, ok := Mapper["AccountChangeSet"]
	if ok {
		if acc.New == nil || acc.Encode == nil || acc.Decode == nil || acc.Find == nil {
			t.Fatalf("AccountChangeSet mapper entry incomplete")
		}
		cs := acc.New()
		if cs.KeySize() != length.Addr {
			t.Fatalf("expected account changeset keyLen %d, got %d", length.Addr, cs.KeySize())
		}
	}
	sto, ok := Mapper["StorageChangeSet"]
	if ok {
		if sto.New == nil || sto.Encode == nil || sto.Decode == nil || sto.Find == nil {
			t.Fatalf("StorageChangeSet mapper entry incomplete")
		}
		cs := sto.New()
		if cs.KeySize() != length.Addr+length.Hash {
			t.Fatalf("expected storage changeset keyLen %d, got %d", length.Addr+length.Hash, cs.KeySize())
		}
	}
}
