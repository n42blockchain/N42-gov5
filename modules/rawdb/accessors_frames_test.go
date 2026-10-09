// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// A frame-referenced block persists as a hash list plus Frames rows, and
// reads back (ReadBlock, the path RPC block lookups take) with its
// transactions decoded from the Frames table, in order, and a TxHash that
// still matches.
func TestFrameBodyWriteReadBlock(t *testing.T) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })
	db := memdb.NewTestDB(t)

	keys := frametest.Keys(t, 5, 7)
	fb := frametest.NewBuilder(keys[0], big.NewInt(94), keys[1:])
	raws, hashes, txs := fb.Frames(t, 3, 40)

	hdr := testHeader(12)
	blk := block.NewFrameBlockFromReceipt(hdr, hashes, txs, nil, []*block.Reward{{Address: types.Address{9}, Amount: uint256.NewInt(5)}}).(*block.Block)
	blk.Body().(*block.Body).FrameData = raws
	plainHash, plainNum := types.Hash{0x55}, uint64(13)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := WriteCanonicalHash(tx, blk.Hash(), 12); err != nil {
			return err
		}
		if err := WriteBlock(tx, blk); err != nil {
			return err
		}
		// An ordinary empty body next to it must not pick up a hash list.
		if err := WriteCanonicalHash(tx, plainHash, plainNum); err != nil {
			return err
		}
		return WriteBody(tx, plainHash, plainNum, &block.Body{})
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		for i, h := range hashes {
			raw, err := ReadFrame(tx, h)
			if err != nil || string(raw) != string(raws[i]) {
				t.Fatalf("frame %d: %v", i, err)
			}
		}
		got := ReadBlock(tx, blk.Hash(), 12)
		if got == nil {
			t.Fatal("ReadBlock nil")
		}
		body := got.Body().(*block.Body)
		if !body.IsFrameBody() || len(body.FrameHashes) != 3 || len(body.Txs) != len(txs) {
			t.Fatalf("frames %d txs %d", len(body.FrameHashes), len(body.Txs))
		}
		for i := range txs {
			if body.Txs[i].Hash() != txs[i].Hash() {
				t.Fatalf("tx %d differs", i)
			}
		}
		if got.Hash() != blk.Hash() || block.BodyTxRoot(body, got.Time()) != got.TxHash() {
			t.Fatal("root/hash mismatch after read")
		}
		if len(body.Rewards) != 1 {
			t.Fatal("rewards lost")
		}
		raw := ReadBodyRAW(tx, blk.Hash(), 12)
		dec, err := DecodeBodyRAW(raw)
		if err != nil || !dec.IsFrameBody() || len(dec.FrameHashes) != 3 {
			t.Fatalf("freezer body form: %v", err)
		}
		plain := ReadCanonicalBodyWithTransactions(tx, plainHash, plainNum)
		if plain == nil || plain.IsFrameBody() || len(plain.Txs) != 0 {
			t.Fatal("plain empty body changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A frame body whose frame bytes are absent and not stored is refused.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		b := &block.Body{FrameHashes: []types.Hash{{0xde, 0xad}}}
		if err := WriteBody(tx, types.Hash{0x77}, 14, b); err == nil {
			t.Fatal("frame body without frame data written")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
