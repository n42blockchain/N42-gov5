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

// TestCachedStateWriterCodeContractAndHistory covers UpdateAccountCode's
// cache mirroring, CreateContract's cache-clear hint logic, and the
// WriteChangeSets/WriteHistory pass-through.
func TestCachedStateWriterCodeContractAndHistory(t *testing.T) {
	db, cache := setupTestDB(t)
	ctx := context.Background()

	var addr types.Address
	addr[0] = 0xF1
	codeHash := types.Hash{0x01}
	code := []byte{0x60, 0x01}

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		inner := NewPlainStateWriterNoHistory(tx)
		w := NewCachedStateWriter(inner, cache)

		if err := w.UpdateAccountCode(addr, codeHash, code); err != nil {
			t.Fatalf("UpdateAccountCode: %v", err)
		}
		if v, ok := cache.Get(modules.Code, codeHash[:]); !ok || string(v) != string(code) {
			t.Fatalf("cache.Get(Code) = %x, %v, want %x, true", v, ok, code)
		}

		// CreateContract with no recorded storage writes and the default
		// CreateContract (mayHaveCachedStorage=true via the plain path)
		// clears the cache; verify the Code entry is gone afterward.
		if err := w.CreateContract(addr); err != nil {
			t.Fatalf("CreateContract: %v", err)
		}
		if _, ok := cache.Get(modules.Code, codeHash[:]); ok {
			t.Fatal("CreateContract did not clear the cache as expected")
		}

		// Re-seed and use the hinted variant with mayHaveCachedStorage=false
		// and no prior storage write for addr: cache must survive.
		if err := w.UpdateAccountCode(addr, codeHash, code); err != nil {
			t.Fatalf("UpdateAccountCode(2): %v", err)
		}
		if err := w.CreateContractHinted(addr, false); err != nil {
			t.Fatalf("CreateContractHinted: %v", err)
		}
		if _, ok := cache.Get(modules.Code, codeHash[:]); !ok {
			t.Fatal("CreateContractHinted(false, no prior write) unexpectedly cleared the cache")
		}

		if err := w.WriteChangeSets(); err != nil {
			t.Fatalf("WriteChangeSets = %v, want nil", err)
		}
		if err := w.WriteHistory(); err != nil {
			t.Fatalf("WriteHistory = %v, want nil", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
