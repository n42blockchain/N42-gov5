// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// enumeratingReader is a minimal StateReader + StorageEnumerator stub for
// exercising PostStateReader.ForEachStorage's base-enumeration path.
type enumeratingReader struct {
	countingReader
	slots map[types.Hash][]byte
}

func (r *enumeratingReader) ForEachStorage(addr types.Address, f func(slot types.Hash, value []byte) bool) error {
	for k, v := range r.slots {
		if !f(k, v) {
			return nil
		}
	}
	return nil
}

func TestPostStateAccountsNilSafe(t *testing.T) {
	var ps *PostState
	if got := ps.Accounts(); got != 0 {
		t.Fatalf("Accounts(nil) = %d, want 0", got)
	}
}

func TestPostStateReaderCodeAndAccountsAndForEachStorage(t *testing.T) {
	var addrA, addrB types.Address
	addrA[0] = 0x01
	addrB[0] = 0x02

	// The zero-value PostState is a legitimate "empty snapshot": no
	// accounts, storage, wipes, or code captured.
	ps := &PostState{}
	if got := ps.Accounts(); got != 0 {
		t.Fatalf("Accounts(empty) = %d, want 0", got)
	}

	base := &enumeratingReader{slots: map[types.Hash][]byte{
		{0x10}: {0xAA},
		{0x11}: {0xBB},
	}}
	r := NewPostStateReader(ps, base)

	// ReadAccountCode/CodeSize fall through to base when the post snapshot
	// has no code entry.
	code, err := r.ReadAccountCode(addrA, types.Hash{0x20})
	if err != nil || code != nil {
		t.Fatalf("ReadAccountCode(fallthrough) = %v, %v, want nil, nil", code, err)
	}
	size, err := r.ReadAccountCodeSize(addrA, types.Hash{0x20})
	if err != nil || size != 0 {
		t.Fatalf("ReadAccountCodeSize(fallthrough) = %d, %v, want 0, nil", size, err)
	}

	// ForEachStorage with no overlay slots and no wipe enumerates the base.
	seen := map[types.Hash][]byte{}
	if err := r.ForEachStorage(addrB, func(k types.Hash, v []byte) bool {
		seen[k] = v
		return true
	}); err != nil {
		t.Fatalf("ForEachStorage: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("ForEachStorage visited %d slots, want 2", len(seen))
	}

	// A base without StorageEnumerator yields ErrNoStorageEnumeration.
	plainBase := &countingReader{}
	r2 := NewPostStateReader(ps, plainBase)
	if err := r2.ForEachStorage(addrB, func(types.Hash, []byte) bool { return true }); err != ErrNoStorageEnumeration {
		t.Fatalf("ForEachStorage(no enumerator) = %v, want ErrNoStorageEnumeration", err)
	}
}
