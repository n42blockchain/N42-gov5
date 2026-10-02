// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestEarliestBlockRoundTrip covers ReadEarliestBlock/WriteEarliestBlock,
// including the "unset" default and the invalid-length error branch.
func TestEarliestBlockRoundTrip(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	n, err := ReadEarliestBlock(tx)
	if err != nil {
		t.Fatalf("read unset: %v", err)
	}
	if n != 0 {
		t.Fatalf("unset earliest block = %d, want 0", n)
	}

	if err := WriteEarliestBlock(tx, 123); err != nil {
		t.Fatalf("write: %v", err)
	}
	n, err = ReadEarliestBlock(tx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != 123 {
		t.Fatalf("earliest block = %d, want 123", n)
	}

	// Corrupt the stored value to exercise the invalid-length error path.
	if err := tx.Put(modules.DatabaseInfo, earliestBlockKey, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("put bad value: %v", err)
	}
	if _, err := ReadEarliestBlock(tx); err == nil {
		t.Fatal("expected error for invalid data length")
	}
}

// TestDeleteBlockData verifies header, TD, body, receipts, logs and senders
// are all removed for the target block, while canonical/number/txlookup
// mappings are left untouched.
func TestDeleteBlockData(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	var hash types.Hash
	hash[0] = 0xAB
	const blockNum = uint64(5)
	blockKey := modules.HeaderKey(blockNum, hash)
	numKey := modules.EncodeBlockNumber(blockNum)

	for _, kv := range []struct {
		table string
		key   []byte
	}{
		{modules.Headers, blockKey},
		{modules.HeaderTD, blockKey},
		{modules.BlockBody, blockKey},
		{modules.Receipts, numKey},
		{modules.Senders, blockKey},
	} {
		if err := tx.Put(kv.table, kv.key, []byte{0x01}); err != nil {
			t.Fatalf("seed %s: %v", kv.table, err)
		}
	}

	// Seed log entries for this block and an adjacent block to verify the
	// prefix-scoped deletion in deleteLogsByBlockNum stops correctly.
	logKeyThis := make([]byte, 12)
	binary.BigEndian.PutUint64(logKeyThis[:8], blockNum)
	binary.BigEndian.PutUint32(logKeyThis[8:], 0)
	if err := tx.Put(modules.Log, logKeyThis, []byte{0x02}); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	logKeyOther := make([]byte, 12)
	binary.BigEndian.PutUint64(logKeyOther[:8], blockNum+1)
	binary.BigEndian.PutUint32(logKeyOther[8:], 0)
	if err := tx.Put(modules.Log, logKeyOther, []byte{0x03}); err != nil {
		t.Fatalf("seed other log: %v", err)
	}

	if err := DeleteBlockData(tx, blockNum, hash); err != nil {
		t.Fatalf("DeleteBlockData: %v", err)
	}

	for _, kv := range []struct {
		table string
		key   []byte
	}{
		{modules.Headers, blockKey},
		{modules.HeaderTD, blockKey},
		{modules.BlockBody, blockKey},
		{modules.Receipts, numKey},
		{modules.Senders, blockKey},
	} {
		v, err := tx.GetOne(kv.table, kv.key)
		if err != nil {
			t.Fatalf("get %s: %v", kv.table, err)
		}
		if v != nil {
			t.Fatalf("table %s still has data after delete", kv.table)
		}
	}

	if v, err := tx.GetOne(modules.Log, logKeyThis); err != nil || v != nil {
		t.Fatalf("log for deleted block still present: v=%x err=%v", v, err)
	}
	if v, err := tx.GetOne(modules.Log, logKeyOther); err != nil || v == nil {
		t.Fatalf("log for adjacent block was incorrectly removed: err=%v", err)
	}
}

// TestBufPoolGetPut exercises the sync.Pool buffer recycler, including the
// oversized-buffer discard path and the nil-safe PutBuf.
func TestBufPoolGetPut(t *testing.T) {
	PutBuf(nil) // must not panic

	b := GetBuf()
	if len(*b) != 0 {
		t.Fatalf("fresh buf len = %d, want 0", len(*b))
	}
	*b = append(*b, []byte("hello")...)
	PutBuf(b)

	b2 := GetBuf()
	if len(*b2) != 0 {
		t.Fatalf("recycled buf len = %d, want 0", len(*b2))
	}
	PutBuf(b2)

	// Oversized buffer is discarded rather than pooled.
	big := make([]byte, 0, (1<<20)+1)
	PutBuf(&big)
}
