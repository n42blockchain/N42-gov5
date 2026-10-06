// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// trie_overlay.go had no test file at all: TrieOverlay's btree write-back and
// the overlayTx/mergedCursor/mergedDupCursor wrappers that intercept
// TrieOfAccounts/TrieOfStorage were all at 0%. This covers: Handles/Put/
// Delete/Get/Len/FlushTo on the overlay itself; WrapTrieOverlay's passthrough
// for a non-intercepted table and interception for the two trie tables (Put,
// Delete, GetOne, Has); the merged cursor's First/Seek/Next over base ∪
// overlay with tombstones masking base rows and overlay winning on equal
// keys; SeekExact; and the "unsupported" error surface the trie loader never
// calls (Prev/Last/Current/Count and the dup-specific CursorDupSort methods).

package commitment

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func newOverlayTestTx(t *testing.T) kv.RwTx {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Rollback)
	return tx
}

func TestTrieOverlayBasic(t *testing.T) {
	o := NewTrieOverlay()
	if !o.Handles(modules.TrieOfAccounts) || !o.Handles(modules.TrieOfStorage) {
		t.Fatal("overlay must handle both trie tables")
	}
	if o.Handles(modules.HashedAccounts) {
		t.Fatal("overlay must not handle an unrelated table")
	}
	if o.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 on a fresh overlay", o.Len())
	}

	o.Put(modules.TrieOfAccounts, []byte("a1"), []byte("v1"))
	o.Put(modules.TrieOfStorage, []byte("s1"), []byte("v2"))
	if o.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", o.Len())
	}
	if v, ok := o.Get(modules.TrieOfAccounts, []byte("a1")); !ok || string(v) != "v1" {
		t.Fatalf("Get(a1) = (%q,%v), want (v1,true)", v, ok)
	}
	if _, ok := o.Get(modules.TrieOfAccounts, []byte("missing")); ok {
		t.Fatal("Get on an absent key must report !ok")
	}

	o.Delete(modules.TrieOfAccounts, []byte("a1"))
	v, ok := o.Get(modules.TrieOfAccounts, []byte("a1"))
	if !ok {
		t.Fatal("a pending delete must still report ok=true (tombstone)")
	}
	if v != nil {
		t.Fatalf("a pending delete's value must be nil, got %q", v)
	}

	tx := newOverlayTestTx(t)
	if err := o.FlushTo(tx); err != nil {
		t.Fatalf("FlushTo: %v", err)
	}
	if o.Len() != 0 {
		t.Fatalf("Len() after FlushTo = %d, want 0 (cleared)", o.Len())
	}
	// a1 was deleted before flush -> never lands in the base table.
	if v, _ := tx.GetOne(modules.TrieOfAccounts, []byte("a1")); v != nil {
		t.Fatalf("expected a1 to be absent after flush, got %q", v)
	}
	if v, _ := tx.GetOne(modules.TrieOfStorage, []byte("s1")); string(v) != "v2" {
		t.Fatalf("expected s1=v2 in the base table after flush, got %q", v)
	}
}

func TestOverlayTxPassthroughAndIntercept(t *testing.T) {
	tx := newOverlayTestTx(t)
	// Seed the base table directly so overlay-vs-base interactions are visible.
	if err := tx.Put(modules.TrieOfAccounts, []byte("base1"), []byte("baseval")); err != nil {
		t.Fatal(err)
	}
	// A non-intercepted table: writes go straight through.
	if err := tx.Put(modules.HashedAccounts, []byte("h1"), []byte("hv")); err != nil {
		t.Fatal(err)
	}

	o := NewTrieOverlay()
	wrapped := WrapTrieOverlay(tx, o)

	// Passthrough table unaffected by the overlay.
	if err := wrapped.Put(modules.HashedAccounts, []byte("h2"), []byte("hv2")); err != nil {
		t.Fatal(err)
	}
	if v, err := wrapped.GetOne(modules.HashedAccounts, []byte("h2")); err != nil || string(v) != "hv2" {
		t.Fatalf("passthrough GetOne(h2) = (%q,%v)", v, err)
	}

	// Intercepted table: Put/GetOne/Has go through the overlay, not the base tx.
	if err := wrapped.Put(modules.TrieOfAccounts, []byte("base1"), []byte("overridden")); err != nil {
		t.Fatal(err)
	}
	v, err := wrapped.GetOne(modules.TrieOfAccounts, []byte("base1"))
	if err != nil || string(v) != "overridden" {
		t.Fatalf("GetOne(base1) through overlay = (%q,%v), want overridden", v, err)
	}
	// The underlying tx must be untouched until FlushTo.
	if raw, _ := tx.GetOne(modules.TrieOfAccounts, []byte("base1")); string(raw) != "baseval" {
		t.Fatalf("base tx must be unmodified pre-flush, got %q", raw)
	}

	has, err := wrapped.Has(modules.TrieOfAccounts, []byte("base1"))
	if err != nil || !has {
		t.Fatalf("Has(base1) = (%v,%v), want (true,nil)", has, err)
	}

	if err := wrapped.Delete(modules.TrieOfAccounts, []byte("base1")); err != nil {
		t.Fatal(err)
	}
	has, err = wrapped.Has(modules.TrieOfAccounts, []byte("base1"))
	if err != nil || has {
		t.Fatalf("Has(base1) after overlay delete = (%v,%v), want (false,nil)", has, err)
	}
	v, err = wrapped.GetOne(modules.TrieOfAccounts, []byte("base1"))
	if err != nil || v != nil {
		t.Fatalf("GetOne(base1) after overlay delete = (%q,%v), want (nil,nil)", v, err)
	}

	// A key the overlay never touched falls through to the base tx.
	has, err = wrapped.Has(modules.TrieOfAccounts, []byte("untouched"))
	if err != nil || has {
		t.Fatalf("Has(untouched) = (%v,%v), want (false,nil)", has, err)
	}
}

func TestMergedCursorIteration(t *testing.T) {
	tx := newOverlayTestTx(t)
	// Base rows: a, c, e.
	for _, kv2 := range [][2]string{{"a", "base-a"}, {"c", "base-c"}, {"e", "base-e"}} {
		if err := tx.Put(modules.TrieOfAccounts, []byte(kv2[0]), []byte(kv2[1])); err != nil {
			t.Fatal(err)
		}
	}
	o := NewTrieOverlay()
	// Overlay: b is new, c is overridden (equal-key: overlay wins), e is
	// tombstoned (deleted), d is new.
	o.Put(modules.TrieOfAccounts, []byte("b"), []byte("ov-b"))
	o.Put(modules.TrieOfAccounts, []byte("c"), []byte("ov-c"))
	o.Delete(modules.TrieOfAccounts, []byte("e"))
	o.Put(modules.TrieOfAccounts, []byte("d"), []byte("ov-d"))

	wrapped := WrapTrieOverlay(tx, o)
	cur, err := wrapped.Cursor(modules.TrieOfAccounts)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()

	var gotK, gotV []string
	for k, v, err := cur.First(); k != nil; k, v, err = cur.Next() {
		if err != nil {
			t.Fatal(err)
		}
		gotK = append(gotK, string(k))
		gotV = append(gotV, string(v))
	}
	wantK := []string{"a", "b", "c", "d"} // e is tombstoned, must be skipped
	wantV := []string{"base-a", "ov-b", "ov-c", "ov-d"}
	if len(gotK) != len(wantK) {
		t.Fatalf("got keys %v, want %v", gotK, wantK)
	}
	for i := range wantK {
		if gotK[i] != wantK[i] || gotV[i] != wantV[i] {
			t.Fatalf("at %d: got (%s,%s), want (%s,%s)", i, gotK[i], gotV[i], wantK[i], wantV[i])
		}
	}

	// SeekExact through the merged cursor on a non-dup table.
	cur2, err := wrapped.Cursor(modules.TrieOfAccounts)
	if err != nil {
		t.Fatal(err)
	}
	defer cur2.Close()
	sc, ok := cur2.(interface {
		SeekExact([]byte) ([]byte, []byte, error)
	})
	if !ok {
		t.Fatal("merged cursor must implement SeekExact")
	}
	if k, v, err := sc.SeekExact([]byte("c")); err != nil || string(k) != "c" || string(v) != "ov-c" {
		t.Fatalf("SeekExact(c) = (%q,%q,%v), want (c,ov-c,nil)", k, v, err)
	}
	if k, v, err := sc.SeekExact([]byte("e")); err != nil || k != nil || v != nil {
		t.Fatalf("SeekExact(e) (tombstoned) = (%q,%q,%v), want (nil,nil,nil)", k, v, err)
	}
	if k, v, err := sc.SeekExact([]byte("zzz")); err != nil || k != nil || v != nil {
		t.Fatalf("SeekExact(zzz) (absent) = (%q,%q,%v), want (nil,nil,nil)", k, v, err)
	}

	// Unsupported methods on the plain merged cursor must fail loudly.
	if _, _, err := cur.(interface {
		Prev() ([]byte, []byte, error)
	}).Prev(); err == nil {
		t.Fatal("expected Prev to be unsupported")
	}
	type unsupported interface {
		Last() ([]byte, []byte, error)
		Current() ([]byte, []byte, error)
		Count() (uint64, error)
	}
	u := cur.(unsupported)
	if _, _, err := u.Last(); err == nil {
		t.Fatal("expected Last to be unsupported")
	}
	if _, _, err := u.Current(); err == nil {
		t.Fatal("expected Current to be unsupported")
	}
	if _, err := u.Count(); err == nil {
		t.Fatal("expected Count to be unsupported")
	}
}

func TestMergedDupCursorUnsupported(t *testing.T) {
	tx := newOverlayTestTx(t)
	if err := tx.Put(modules.TrieOfStorage, []byte("k1"), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	o := NewTrieOverlay()
	o.Put(modules.TrieOfStorage, []byte("k2"), []byte("v2"))
	wrapped := WrapTrieOverlay(tx, o)

	dc, err := wrapped.CursorDupSort(modules.TrieOfStorage)
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	// Flat iteration still works through the dup cursor.
	k, v, err := dc.First()
	if err != nil || string(k) != "k1" || string(v) != "v1" {
		t.Fatalf("First() = (%q,%q,%v), want (k1,v1,nil)", k, v, err)
	}
	k, v, err = dc.Next()
	if err != nil || string(k) != "k2" || string(v) != "v2" {
		t.Fatalf("Next() = (%q,%q,%v), want (k2,v2,nil)", k, v, err)
	}

	if _, _, err := dc.SeekBothExact(nil, nil); err == nil {
		t.Fatal("expected SeekBothExact to be unsupported")
	}
	if _, err := dc.SeekBothRange(nil, nil); err == nil {
		t.Fatal("expected SeekBothRange to be unsupported")
	}
	if _, err := dc.FirstDup(); err == nil {
		t.Fatal("expected FirstDup to be unsupported")
	}
	if _, _, err := dc.NextDup(); err == nil {
		t.Fatal("expected NextDup to be unsupported")
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
