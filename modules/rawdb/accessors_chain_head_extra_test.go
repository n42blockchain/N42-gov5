// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// ReadCurrentBlockNumber/ReadCurrentHeader resolve through HeadHeaderHash,
// and ReadCurrentBlock resolves through HeadBlockHash.
func TestReadCurrentHeaderAndBlock(t *testing.T) {
	withChaindataTables(t)
	_, tx := memdb.NewTestTx(t)

	if got := ReadCurrentBlockNumber(tx); got != nil {
		t.Fatalf("ReadCurrentBlockNumber (empty db) = %v, want nil", got)
	}
	if got := ReadCurrentHeader(tx); got != nil {
		t.Fatalf("ReadCurrentHeader (empty db) = %v, want nil", got)
	}
	if got := ReadCurrentBlock(tx); got != nil {
		t.Fatalf("ReadCurrentBlock (empty db) = %v, want nil", got)
	}

	h := testHeader(30)
	WriteHeader(tx, h)
	hash := h.Hash()
	if err := WriteHeadHeaderHash(tx, hash); err != nil {
		t.Fatal(err)
	}

	num := ReadCurrentBlockNumber(tx)
	if num == nil || *num != 30 {
		t.Fatalf("ReadCurrentBlockNumber = %v, want 30", num)
	}
	hdr := ReadCurrentHeader(tx)
	if hdr == nil || hdr.Number.Uint64() != 30 {
		t.Fatalf("ReadCurrentHeader = %v", hdr)
	}

	// ReadCurrentBlock needs both the header AND a canonical body at the
	// HeadBlockKey hash.
	if err := WriteCanonicalHash(tx, hash, 30); err != nil {
		t.Fatal(err)
	}
	if err := WriteBody(tx, hash, 30, &block.Body{Txs: benchWriteTxs(1)}); err != nil {
		t.Fatal(err)
	}
	WriteHeadBlockHash(tx, hash)

	blk := ReadCurrentBlock(tx)
	if blk == nil || blk.Number64().Uint64() != 30 {
		t.Fatalf("ReadCurrentBlock = %v", blk)
	}
}

// WriteHotStuffCommittedHead/ReadHotStuffCommittedHead round trip, defaulting
// to the zero hash when unset.
func TestHotStuffCommittedHeadRoundTrip(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	if got := ReadHotStuffCommittedHead(tx); got != (types.Hash{}) {
		t.Fatalf("ReadHotStuffCommittedHead (unset) = %v, want zero", got)
	}

	want := types.Hash{0x42}
	if err := WriteHotStuffCommittedHead(tx, want); err != nil {
		t.Fatal(err)
	}
	if got := ReadHotStuffCommittedHead(tx); got != want {
		t.Fatalf("ReadHotStuffCommittedHead = %v, want %v", got, want)
	}
}

// PoA snapshot storage is a direct key->blob mapping.
func TestPoaSnapshotRoundTrip(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	hash := types.Hash{0x77}

	if got, err := GetPoaSnapshot(tx, hash); err != nil || got != nil {
		t.Fatalf("GetPoaSnapshot (missing) = %v, %v", got, err)
	}
	if err := StorePoaSnapshot(tx, hash, []byte("snap")); err != nil {
		t.Fatal(err)
	}
	got, err := GetPoaSnapshot(tx, hash)
	if err != nil || string(got) != "snap" {
		t.Fatalf("GetPoaSnapshot = %q, %v", got, err)
	}
}
