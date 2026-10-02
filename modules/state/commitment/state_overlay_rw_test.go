// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers WrapStateOverlayRW's GetOne/Has (the main-thread write-path wrapper
// used to fill the overlay via the existing serial write logic before fan-out)
// and StateOverlay.Len across all four tables.

package commitment

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestStateOverlayRwTxGetOneHas(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	addr := ovAddr(0x77)
	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := tx.Put(modules.HashedAccounts, addr, []byte("base-acct")); err != nil {
		t.Fatal(err)
	}

	ov := NewStateOverlay()
	wrapped := WrapStateOverlayRW(tx, ov)

	// Base value visible before any overlay write.
	v, err := wrapped.GetOne(modules.HashedAccounts, addr)
	if err != nil || string(v) != "base-acct" {
		t.Fatalf("GetOne(addr) = (%q,%v), want (base-acct,nil)", v, err)
	}
	has, err := wrapped.Has(modules.HashedAccounts, addr)
	if err != nil || !has {
		t.Fatalf("Has(addr) = (%v,%v), want (true,nil)", has, err)
	}

	// Writing through the wrapper routes into the overlay, not the base tx.
	if err := wrapped.Put(modules.HashedAccounts, addr, []byte("overlay-acct")); err != nil {
		t.Fatal(err)
	}
	if raw, _ := tx.GetOne(modules.HashedAccounts, addr); string(raw) != "base-acct" {
		t.Fatalf("base tx must be unmodified, got %q", raw)
	}
	v, err = wrapped.GetOne(modules.HashedAccounts, addr)
	if err != nil || string(v) != "overlay-acct" {
		t.Fatalf("GetOne(addr) after overlay put = (%q,%v), want (overlay-acct,nil)", v, err)
	}

	if err := wrapped.Delete(modules.HashedAccounts, addr); err != nil {
		t.Fatal(err)
	}
	has, err = wrapped.Has(modules.HashedAccounts, addr)
	if err != nil || has {
		t.Fatalf("Has(addr) after overlay delete = (%v,%v), want (false,nil)", has, err)
	}

	// Passthrough table.
	other := ovAddr(0x88)
	if err := tx.Put(modules.Code, other, []byte("codeval")); err != nil {
		t.Fatal(err)
	}
	v, err = wrapped.GetOne(modules.Code, other)
	if err != nil || string(v) != "codeval" {
		t.Fatalf("GetOne passthrough = (%q,%v), want (codeval,nil)", v, err)
	}
	has, err = wrapped.Has(modules.Code, other)
	if err != nil || !has {
		t.Fatalf("Has passthrough = (%v,%v), want (true,nil)", has, err)
	}
}

func TestStateOverlayLenAllFourTables(t *testing.T) {
	ov := NewStateOverlay()
	if ov.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", ov.Len())
	}
	ov.Put(modules.HashedAccounts, []byte("a"), []byte("1"))
	ov.Put(modules.HashedStorage, []byte("b"), []byte("2"))
	ov.Put(modules.TrieOfAccounts, []byte("c"), []byte("3"))
	ov.Put(modules.TrieOfStorage, []byte("d"), []byte("4"))
	if ov.Len() != 4 {
		t.Fatalf("Len() = %d, want 4 across all four tables", ov.Len())
	}
}
