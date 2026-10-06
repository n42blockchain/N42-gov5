// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestExecutedResultRoundTrip(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var bloom block.Bloom
	bloom[0], bloom[255] = 0x81, 0x7e
	want := ExecutedResult{Root: types.Hash{1, 2, 3}, ReceiptHash: types.Hash{4, 5}, Bloom: bloom, GasUsed: 3423000000}
	h := types.Hash{9}
	if _, ok, _ := ReadExecutedResult(tx, h); ok {
		t.Fatal("unexpected result before the write")
	}
	if err := WriteExecutedResult(tx, h, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadExecutedResult(tx, h)
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("round trip: %+v != %+v", got, want)
	}
}
