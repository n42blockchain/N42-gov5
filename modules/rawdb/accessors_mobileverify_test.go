// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestMobileAnchorRoundTrip(t *testing.T) {
	db := memdb.NewTestDB(t)
	root := types.Hash{0x01, 0x02, 0x03}
	rec := MobileAnchorRecord{Epoch: 7, Root: root, HeadBlock: 13220000, TimeMs: 1784000000000}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteMobileAnchor(tx, rec)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got, ok := ReadMobileAnchor(tx, 7)
		if !ok || got != rec {
			t.Fatalf("read = (%+v, %v), want %+v", got, ok, rec)
		}
		if _, ok := ReadMobileAnchor(tx, 8); ok {
			t.Fatal("read a missing epoch")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecentMobileAnchorsNewestFirst(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for e := uint64(1); e <= 5; e++ {
			r := MobileAnchorRecord{Epoch: e, Root: types.Hash{byte(e)}, HeadBlock: e * 100, TimeMs: e}
			if err := WriteMobileAnchor(tx, r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		recs, err := RecentMobileAnchors(tx, 3)
		if err != nil {
			return err
		}
		if len(recs) != 3 || recs[0].Epoch != 5 || recs[2].Epoch != 3 {
			t.Fatalf("recent = %+v, want epochs 5,4,3", recs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
