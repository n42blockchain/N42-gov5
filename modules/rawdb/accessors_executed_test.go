// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
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
	want := ExecutedResult{Root: types.Hash{1, 2, 3}, ReceiptHash: types.Hash{4, 5}, Bloom: bloom, GasUsed: 3423000000, RequestsHash: types.Hash{0xe3, 7}}
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

// A record written before the requests hash was stored reads back with
// sha256(""): such a block produced no requests (AM24 stamped that value).
func TestExecutedResultReadsARecordWithoutRequestsHash(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	h := types.Hash{8}
	want := ExecutedResult{Root: types.Hash{1}, ReceiptHash: types.Hash{2}, GasUsed: 21000, RequestsHash: types.Hash{3}}
	if err := WriteExecutedResult(tx, h, want); err != nil {
		t.Fatal(err)
	}
	v, err := tx.GetOne(modules.ExecutedResult, h[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Put(modules.ExecutedResult, h[:], append([]byte(nil), v[:executedResultLenV1]...)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadExecutedResult(tx, h)
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	want.RequestsHash = EmptyRequestsHash
	if got != want {
		t.Fatalf("old record: %+v != %+v", got, want)
	}
	if EmptyRequestsHash != types.HexToHash("0xe3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855") {
		t.Fatalf("EmptyRequestsHash %s", EmptyRequestsHash.Hex())
	}
}
