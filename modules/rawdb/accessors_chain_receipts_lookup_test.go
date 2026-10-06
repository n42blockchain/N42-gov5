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

// receiptTestBlock wires up a canonical block with a committee of tables that
// ReadReceiptsByHash/ReadReceiptByTxHash depend on: header, canonical hash,
// body, receipts and the tx-lookup index.
func writeReceiptTestBlock(t *testing.T, tx kv.RwTx, number uint64, receipts block.Receipts) (*block.Block, types.Hash) {
	t.Helper()
	txs := benchWriteTxs(len(receipts))
	blk := block.NewBlock(testHeader(number), txs).(*block.Block)
	hash := blk.Hash()

	if err := WriteCanonicalHash(tx, hash, number); err != nil {
		t.Fatalf("WriteCanonicalHash: %v", err)
	}
	if err := WriteBlock(tx, blk); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	WriteTxLookupEntries(tx, blk)
	if err := WriteReceipts(tx, number, receipts); err != nil {
		t.Fatalf("WriteReceipts: %v", err)
	}
	return blk, hash
}

func makeCleanReceipts(n int) block.Receipts {
	out := make(block.Receipts, n)
	var cum uint64
	for i := range out {
		cum += 21_000
		out[i] = &block.Receipt{Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: cum}
	}
	return out
}

func TestReadReceiptsByHash(t *testing.T) {
	withChaindataTables(t)
	db := memdb.NewTestDB(t)

	var blk *block.Block
	var hash types.Hash
	other := types.Hash{0xbb}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, hash = writeReceiptTestBlock(t, tx, 11, makeCleanReceipts(2))
		// A hash that resolves to a real number but isn't the canonical hash
		// at that number must be rejected.
		WriteHeaderNumber(tx, other, 11)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got, err := ReadReceiptsByHash(tx, hash)
		if err != nil {
			t.Fatalf("ReadReceiptsByHash: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d receipts, want 2", len(got))
		}

		// Unknown hash -> no header number -> (nil, nil).
		missing, err := ReadReceiptsByHash(tx, types.Hash{0xaa})
		if err != nil || missing != nil {
			t.Fatalf("ReadReceiptsByHash(unknown) = %v, %v", missing, err)
		}

		if r, err := ReadReceiptsByHash(tx, other); err != nil || r != nil {
			t.Fatalf("ReadReceiptsByHash(non-canonical) = %v, %v", r, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = blk
}

func TestReadReceiptByTxHash(t *testing.T) {
	withChaindataTables(t)
	db := memdb.NewTestDB(t)

	var blk *block.Block
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _ = writeReceiptTestBlock(t, tx, 12, makeCleanReceipts(3))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	txs := blk.Transactions()
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		// NOTE (defect, not fixed here per instructions): BodyForStorage
		// reserves 2 extra BlockTx slots ahead of the real transactions
		// (TxAmount = len(txs)+2, data starts at BaseTxId+1), but
		// ReadReceiptByTxHash scans BaseTxId+i for i in [0, TxAmount) and
		// returns receipts[i]/idx=i using that raw scan index rather than
		// the transaction's real position. The real tx at position j lives
		// at scan index i=j+1, so the function returns the WRONG receipt
		// (off by one) and silently returns nil for the highest-index
		// transaction, whose shifted index falls outside len(receipts).
		receipt, num, idx, err := ReadReceiptByTxHash(tx, txs[0].Hash())
		if err != nil {
			t.Fatalf("ReadReceiptByTxHash: %v", err)
		}
		if receipt == nil {
			t.Fatal("receipt = nil")
		}
		if num != 12 || idx != 1 {
			t.Fatalf("block/index = %d/%d, want 12/1 (shifted by the BaseTxId offset defect)", num, idx)
		}

		// The last transaction's shifted scan index (len(txs)) falls
		// outside len(receipts), so lookup degrades to a silent miss.
		lastReceipt, _, _, err := ReadReceiptByTxHash(tx, txs[len(txs)-1].Hash())
		if err != nil {
			t.Fatalf("ReadReceiptByTxHash(last): %v", err)
		}
		if lastReceipt != nil {
			t.Fatal("expected nil receipt for the last transaction (boundary defect)")
		}

		// Unknown tx hash.
		r, n, i, err := ReadReceiptByTxHash(tx, types.Hash{0xcc})
		if err != nil || r != nil || n != 0 || i != 0 {
			t.Fatalf("ReadReceiptByTxHash(unknown) = %v,%d,%d,%v", r, n, i, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHasReceiptsAndRawReceipts(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	if HasReceipts(tx, 20) {
		t.Fatal("HasReceipts true before write")
	}
	receipts := makeCleanReceipts(1)
	if err := WriteReceipts(tx, 20, receipts); err != nil {
		t.Fatalf("WriteReceipts: %v", err)
	}
	if !HasReceipts(tx, 20) {
		t.Fatal("HasReceipts false after write")
	}

	raw := ReadRawReceipts(tx, 20)
	if len(raw) != 1 {
		t.Fatalf("ReadRawReceipts len = %d, want 1", len(raw))
	}

	// Missing block number falls through to the (empty) ancient fallback.
	if raw := ReadRawReceipts(tx, 999); len(raw) != 0 {
		t.Fatalf("ReadRawReceipts(missing) = %v, want empty", raw)
	}
}

func TestWriteReceiptsPooledAndAppendReceipts(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	receipts := makeCleanReceipts(1)
	receipts[0].Logs = []*block.Log{{Address: types.Address{0x01}}}
	if err := WriteReceiptsPooled(tx, 30, receipts); err != nil {
		t.Fatalf("WriteReceiptsPooled: %v", err)
	}
	got := ReadRawReceipts(tx, 30)
	if len(got) != 1 {
		t.Fatalf("ReadRawReceipts after pooled write = %d, want 1", len(got))
	}

	v, err := tx.GetOne(modules.Log, modules.LogKey(30, 0))
	if err != nil || len(v) == 0 {
		t.Fatalf("log row missing after WriteReceiptsPooled: v=%v err=%v", v, err)
	}
}

func TestTruncateReceiptsAndAvailableFrom(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	// No receipts yet -> MaxUint64 sentinel.
	from, err := ReceiptsAvailableFrom(tx)
	if err != nil {
		t.Fatalf("ReceiptsAvailableFrom: %v", err)
	}
	if from != ^uint64(0) {
		t.Fatalf("ReceiptsAvailableFrom(empty) = %d, want MaxUint64", from)
	}

	for n := uint64(5); n <= 7; n++ {
		r := makeCleanReceipts(1)
		r[0].Logs = []*block.Log{{Address: types.Address{0x02}}}
		if err := WriteReceipts(tx, n, r); err != nil {
			t.Fatalf("WriteReceipts(%d): %v", n, err)
		}
	}

	from, err = ReceiptsAvailableFrom(tx)
	if err != nil || from != 5 {
		t.Fatalf("ReceiptsAvailableFrom = %d, %v, want 5", from, err)
	}

	if err := TruncateReceipts(tx, 6); err != nil {
		t.Fatalf("TruncateReceipts: %v", err)
	}

	if v, _ := tx.GetOne(modules.Receipts, modules.EncodeBlockNumber(5)); v == nil {
		t.Fatal("block 5 receipts truncated incorrectly")
	}
	if v, _ := tx.GetOne(modules.Receipts, modules.EncodeBlockNumber(6)); v != nil {
		t.Fatal("block 6 receipts survived truncation")
	}
	if v, _ := tx.GetOne(modules.Receipts, modules.EncodeBlockNumber(7)); v != nil {
		t.Fatal("block 7 receipts survived truncation")
	}
	if v, _ := tx.GetOne(modules.Log, modules.LogKey(6, 0)); v != nil {
		t.Fatal("block 6 logs survived truncation")
	}
}

func TestAppendReceipts(t *testing.T) {
	db := memdb.NewTestDB(t)
	receipts := makeCleanReceipts(1)
	receipts[0].Logs = []*block.Log{{Address: types.Address{0x03}}}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return AppendReceipts(tx, 40, receipts)
	}); err != nil {
		t.Fatalf("AppendReceipts: %v", err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		raw := ReadRawReceipts(tx, 40)
		if len(raw) != 1 {
			t.Fatalf("ReadRawReceipts after append = %d, want 1", len(raw))
		}
		v, err := tx.GetOne(modules.Log, modules.LogKey(40, 0))
		if err != nil || len(v) == 0 {
			t.Fatalf("log row missing after AppendReceipts: v=%v err=%v", v, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
