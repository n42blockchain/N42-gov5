// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers the remaining 0% corners of state_overlay.go that
// TestStateDupCursorMatchesAppliedRaw and TestFlushToUpsertMatchesDeleteBeforePut
// leave untouched: stateOverlayTx.GetOne/Has (the read-only per-worker wrapper's
// point-read surface) and the stateDupCursor "unsupported" error methods the
// trie loader never calls.

package commitment

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestStateOverlayTxGetOneHas(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	addr := ovAddr(0x55)
	wtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other := ovAddr(0x66)
	if err := wtx.Put(modules.HashedAccounts, addr, []byte("base-acct")); err != nil {
		t.Fatal(err)
	}
	if err := wtx.Put(modules.Code, other, []byte("codeval")); err != nil {
		t.Fatal(err)
	}
	if err := wtx.Commit(); err != nil {
		t.Fatal(err)
	}

	ov := NewStateOverlay()
	roTx, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()
	wrapped := WrapStateOverlay(roTx, ov)

	// Base value visible through the wrapper before any overlay write.
	v, err := wrapped.GetOne(modules.HashedAccounts, addr)
	if err != nil || string(v) != "base-acct" {
		t.Fatalf("GetOne(addr) = (%q,%v), want (base-acct,nil)", v, err)
	}
	has, err := wrapped.Has(modules.HashedAccounts, addr)
	if err != nil || !has {
		t.Fatalf("Has(addr) = (%v,%v), want (true,nil)", has, err)
	}

	// Overlay override wins.
	ov.Put(modules.HashedAccounts, addr, []byte("overlay-acct"))
	v, err = wrapped.GetOne(modules.HashedAccounts, addr)
	if err != nil || string(v) != "overlay-acct" {
		t.Fatalf("GetOne(addr) after overlay put = (%q,%v), want (overlay-acct,nil)", v, err)
	}

	// Overlay tombstone masks the base row.
	ov.Delete(modules.HashedAccounts, addr)
	has, err = wrapped.Has(modules.HashedAccounts, addr)
	if err != nil || has {
		t.Fatalf("Has(addr) after overlay delete = (%v,%v), want (false,nil)", has, err)
	}
	v, err = wrapped.GetOne(modules.HashedAccounts, addr)
	if err != nil || v != nil {
		t.Fatalf("GetOne(addr) after overlay delete = (%q,%v), want (nil,nil)", v, err)
	}

	// A table the overlay does not handle passes straight through.
	v, err = wrapped.GetOne(modules.Code, other)
	if err != nil || string(v) != "codeval" {
		t.Fatalf("GetOne passthrough = (%q,%v), want (codeval,nil)", v, err)
	}
}

func TestStateDupCursorUnsupportedMethods(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	ov := NewStateOverlay()
	roTx, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()
	wrapped := WrapStateOverlay(roTx, ov)

	dc, err := wrapped.CursorDupSort(modules.HashedStorage)
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	if _, _, err := dc.SeekBothExact(nil, nil); err == nil {
		t.Fatal("expected SeekBothExact to be unsupported")
	}
	if _, err := dc.FirstDup(); err == nil {
		t.Fatal("expected FirstDup to be unsupported")
	}
	if _, _, err := dc.NextNoDup(); err == nil {
		t.Fatal("expected NextNoDup to be unsupported")
	}
	if _, _, err := dc.PrevDup(); err == nil {
		t.Fatal("expected PrevDup to be unsupported")
	}
	if _, _, err := dc.PrevNoDup(); err == nil {
		t.Fatal("expected PrevNoDup to be unsupported")
	}
	if _, err := dc.LastDup(); err == nil {
		t.Fatal("expected LastDup to be unsupported")
	}
	if _, err := dc.CountDuplicates(); err == nil {
		t.Fatal("expected CountDuplicates to be unsupported")
	}
}
