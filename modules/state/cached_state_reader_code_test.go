// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// TestCachedStateReaderCodeCachingAndForEachStorage covers ReadAccountCode's
// cache-miss-then-populate path, ReadAccountCodeSize, and the
// ForEachStorage forwarding / ErrNoStorageEnumeration branches.
func TestCachedStateReaderCodeCachingAndForEachStorage(t *testing.T) {
	db, cache := setupTestDB(t)
	ctx := context.Background()

	var addr types.Address
	addr[0] = 0xE1
	code := []byte{0x60, 0x01, 0x60, 0x02}
	codeHash := types.Hash{0x01}

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.Code, codeHash[:], code)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		inner := NewPlainStateReader(tx)
		reader := NewCachedStateReader(inner, cache)

		// First read: cache miss -> inner -> populates cache.
		got, err := reader.ReadAccountCode(addr, codeHash)
		if err != nil || string(got) != string(code) {
			t.Fatalf("ReadAccountCode(miss) = %x, %v, want %x, nil", got, err, code)
		}
		// Second read: cache hit, same result.
		got2, err := reader.ReadAccountCode(addr, codeHash)
		if err != nil || string(got2) != string(code) {
			t.Fatalf("ReadAccountCode(hit) = %x, %v, want %x, nil", got2, err, code)
		}

		size, err := reader.ReadAccountCodeSize(addr, codeHash)
		if err != nil || size != len(code) {
			t.Fatalf("ReadAccountCodeSize = %d, %v, want %d, nil", size, err, len(code))
		}

		// ForEachStorage forwards to inner (PlainStateReader implements
		// StorageEnumerator over the memdb cursor-backed tx).
		if err := reader.ForEachStorage(addr, func(types.Hash, []byte) bool { return true }); err != nil {
			t.Fatalf("ForEachStorage(forward) = %v, want nil", err)
		}

		// A reader wrapping an inner without StorageEnumerator reports
		// ErrNoStorageEnumeration.
		reader2 := NewCachedStateReader(&countingReader{}, cache)
		if err := reader2.ForEachStorage(addr, func(types.Hash, []byte) bool { return true }); err != ErrNoStorageEnumeration {
			t.Fatalf("ForEachStorage(no enumerator) = %v, want ErrNoStorageEnumeration", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
