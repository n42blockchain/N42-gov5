// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// TestPrefillSlot: a source on an empty slot reads through the tree; once a
// prefill is published it answers the prefilled keys from it (hits counted)
// and the rest through the tree, and a prefill set directly wins over the
// slot's.
func TestPrefillSlot(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	db := memdb.NewTestDB(t)
	rc := NewQMDBRootComputer()

	a, b := types.Address{0xb0, 1}, types.Address{0xb0, 2}
	if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{a: mkAccount(1, 10), b: mkAccount(2, 20)}, nil); err != nil {
		t.Fatal(err)
	}
	ka, kb := qmdb.Hash(AccountKeyHash(a)), qmdb.Hash(AccountKeyHash(b))
	want := func(k qmdb.Hash) []byte { return mustLookup(t, rc, k) }
	wa, wb := want(ka), want(kb)

	pf, err := rc.FillBasePrefill(db, [][]qmdb.Hash{{ka}}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var slot PrefillSlot
	src := NewLookupSource(rc, tx).SetPrefillSlot(&slot)
	if v, ok := src.Get(ka); !ok || !bytes.Equal(v, wa) {
		t.Fatalf("empty slot: got %x %v, want %x", v, ok, wa)
	}
	if h, m := pf.Stats(); h != 0 || m != 0 {
		t.Fatalf("an unpublished prefill answered: hits %d misses %d", h, m)
	}
	slot.Set(pf)
	if v, ok := src.Get(ka); !ok || !bytes.Equal(v, wa) {
		t.Fatalf("published slot: got %x %v, want %x", v, ok, wa)
	}
	if v, ok := src.Get(kb); !ok || !bytes.Equal(v, wb) {
		t.Fatalf("published slot, key not prefilled: got %x %v, want %x", v, ok, wb)
	}
	if h, m := pf.Stats(); h != 1 || m != 1 {
		t.Fatalf("slot prefill: hits %d misses %d, want 1 and 1", h, m)
	}

	// A prefill set directly is consulted instead of the slot's.
	direct, err := rc.FillBasePrefill(db, [][]qmdb.Hash{{kb}}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	src.SetPrefill(direct)
	if v, ok := src.Get(kb); !ok || !bytes.Equal(v, wb) {
		t.Fatalf("direct prefill: got %x %v, want %x", v, ok, wb)
	}
	if h, _ := direct.Stats(); h != 1 {
		t.Fatalf("direct prefill hits %d, want 1", h)
	}
	if h, _ := pf.Stats(); h != 1 {
		t.Fatalf("the slot was consulted beside a direct prefill: hits %d", h)
	}
	var nilSlot *PrefillSlot
	nilSlot.Set(pf)
	if nilSlot.Load() != nil {
		t.Fatal("nil slot loaded a prefill")
	}
}
