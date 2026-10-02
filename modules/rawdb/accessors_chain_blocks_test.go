// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func withChaindataTables(t *testing.T) {
	t.Helper()
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })
}

// WriteBlock/WriteBlockPooled write the header and body together; ReadBlock,
// ReadBlockByNumber, ReadBlockByHash and HasBlock all resolve the same block.
func TestWriteBlockAndReadBlockVariants(t *testing.T) {
	withChaindataTables(t)
	db := memdb.NewTestDB(t)
	txs := benchWriteTxs(2)

	blk := block.NewBlock(testHeader(15), txs).(*block.Block)
	hash := blk.Hash()

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := WriteCanonicalHash(tx, hash, 15); err != nil {
			return err
		}
		return WriteBlock(tx, blk)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if !HasBlock(tx, hash, 15) {
			t.Fatal("HasBlock = false")
		}
		if HasBlock(tx, types.Hash{0xff}, 15) {
			t.Fatal("HasBlock = true for unknown hash")
		}

		got := ReadBlock(tx, hash, 15)
		if got == nil {
			t.Fatal("ReadBlock = nil")
		}
		if got.Number64().Uint64() != 15 {
			t.Fatalf("ReadBlock number = %d, want 15", got.Number64().Uint64())
		}

		if got := ReadBlock(tx, types.Hash{0xff}, 15); got != nil {
			t.Fatal("ReadBlock on unknown hash returned non-nil")
		}

		byNum, err := ReadBlockByNumber(tx, 15)
		if err != nil || byNum == nil {
			t.Fatalf("ReadBlockByNumber = %v, %v", byNum, err)
		}
		missing, err := ReadBlockByNumber(tx, 999)
		if err != nil || missing != nil {
			t.Fatalf("ReadBlockByNumber at missing number = %v, %v", missing, err)
		}

		byHash, err := ReadBlockByHash(tx, hash)
		if err != nil || byHash == nil {
			t.Fatalf("ReadBlockByHash = %v, %v", byHash, err)
		}
		byHashMissing, err := ReadBlockByHash(tx, types.Hash{0xaa})
		if err != nil || byHashMissing != nil {
			t.Fatalf("ReadBlockByHash on unknown hash = %v, %v", byHashMissing, err)
		}

		hdr := ReadHeaderByNumber(tx, 15)
		if hdr == nil || hdr.Number.Uint64() != 15 {
			t.Fatalf("ReadHeaderByNumber = %v", hdr)
		}
		if h := ReadHeaderByNumber(tx, 999); h != nil {
			t.Fatal("ReadHeaderByNumber at missing number returned non-nil")
		}

		hdrByHash, err := ReadHeaderByHash(tx, hash)
		if err != nil || hdrByHash == nil {
			t.Fatalf("ReadHeaderByHash = %v, %v", hdrByHash, err)
		}
		hdrByHashMissing, err := ReadHeaderByHash(tx, types.Hash{0xbb})
		if err != nil || hdrByHashMissing != nil {
			t.Fatalf("ReadHeaderByHash on unknown hash = %v, %v", hdrByHashMissing, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// WriteBlockPooled must produce an equally readable block at a new height.
	blk2 := block.NewBlock(testHeader(16), txs).(*block.Block)
	hash2 := blk2.Hash()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := WriteCanonicalHash(tx, hash2, 16); err != nil {
			return err
		}
		return WriteBlockPooled(tx, blk2)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if got := ReadBlock(tx, hash2, 16); got == nil {
			t.Fatal("ReadBlock after WriteBlockPooled = nil")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ReadBlockWithSenders loads the per-tx sender list alongside the block, and
// tolerates a missing/mismatched senders row by returning the block unchanged.
func TestReadBlockWithSenders(t *testing.T) {
	withChaindataTables(t)
	db := memdb.NewTestDB(t)
	txs := benchWriteTxs(2)
	blk := block.NewBlock(testHeader(21), txs).(*block.Block)
	hash := blk.Hash()

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := WriteCanonicalHash(tx, hash, 21); err != nil {
			return err
		}
		return WriteBlock(tx, blk)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		gotBlk, senders, err := ReadBlockWithSenders(tx, hash, 21)
		if err != nil || gotBlk == nil {
			t.Fatalf("ReadBlockWithSenders = %v, %v, %v", gotBlk, senders, err)
		}
		// No senders row was written: length mismatch (0 != len(txs)), block
		// returned as-is.
		if len(senders) != 0 {
			t.Fatalf("senders = %v, want empty", senders)
		}

		gotBlk2, senders2, err := ReadBlockWithSenders(tx, types.Hash{0xcc}, 21)
		if err != nil || gotBlk2 != nil || senders2 != nil {
			t.Fatalf("ReadBlockWithSenders on unknown hash = %v, %v, %v", gotBlk2, senders2, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ReadBlockWithSenders assigns senders back onto each transaction when the
// stored list's length matches the transaction count.
func TestReadBlockWithSendersMatchedLength(t *testing.T) {
	withChaindataTables(t)
	db := memdb.NewTestDB(t)
	txs := benchWriteTxs(2)
	blk := block.NewBlock(testHeader(22), txs).(*block.Block)
	hash := blk.Hash()

	a1 := types.HexToAddress("0x1000000000000000000000000000000000000001")
	a2 := types.HexToAddress("0x2000000000000000000000000000000000000002")
	sendersData := append(append([]byte{}, a1[:]...), a2[:]...)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := WriteCanonicalHash(tx, hash, 22); err != nil {
			return err
		}
		if err := WriteBlock(tx, blk); err != nil {
			return err
		}
		return tx.Put(modules.Senders, modules.BlockBodyKey(22, hash), sendersData)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		gotBlk, senders, err := ReadBlockWithSenders(tx, hash, 22)
		if err != nil || gotBlk == nil {
			t.Fatalf("ReadBlockWithSenders = %v, %v, %v", gotBlk, senders, err)
		}
		if len(senders) != 2 || senders[0] != a1 || senders[1] != a2 {
			t.Fatalf("senders = %v", senders)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
