// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/proto/types_pb"
	"google.golang.org/protobuf/proto"
)

func TestAltSigNativeCarrierAndStorage(t *testing.T) {
	modules.N42Init()
	previous := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = previous })
	tx, err := transaction.SignAltTx(&transaction.AltSigTx{ChainID: 94, Gas: 21000, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(2), Value: uint256.NewInt(3), To: types.Address{19: 0xaa}, Data: []byte{1, 2}, AccessList: transaction.AccessList{{Address: types.Address{19: 0xcc}, StorageKeys: []types.Hash{{31: 1}}}}}, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x32}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tx.EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	body := &block.Body{Txs: []*transaction.Transaction{tx}}
	pb, err := proto.Marshal(body.ToProtoMessage())
	if err != nil {
		t.Fatal(err)
	}
	var decodedPB types_pb.Body
	if err := proto.Unmarshal(pb, &decodedPB); err != nil {
		t.Fatal(err)
	}
	var restoredBody block.Body
	if err := restoredBody.FromProtoMessage(&decodedPB); err != nil {
		t.Fatal(err)
	}
	check := func(txs []*transaction.Transaction) {
		t.Helper()
		if len(txs) != 1 {
			t.Fatal("lost transaction")
		}
		got, err := txs[0].EthEncoded()
		if err != nil || !bytes.Equal(got, raw) || txs[0].Hash() != tx.Hash() || txs[0].Type() != transaction.AltSigTxType || txs[0].From() != nil {
			t.Fatalf("carrier changed transaction: %v", err)
		}
	}
	check(restoredBody.Txs)
	// Standard block RLP deliberately retains its ETH-only decoder. Config-aware
	// performance block decoding is a later slice, not silently enabled here.
	blk := block.NewBlock(testHeader(1), body.Txs).(*block.Block)
	blockBytes, err := blk.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var normalBlock block.Block
	if err := normalBlock.Unmarshal(blockBytes); err == nil {
		t.Fatal("default block decoder admitted offline type")
	}

	db := memdb.NewTestDB(t)
	hash := blk.Hash()
	receipts := block.Receipts{{Type: transaction.AltSigTxType, Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: 21000}}
	if err := db.Update(context.Background(), func(dbtx kv.RwTx) error {
		if err := WriteCanonicalHash(dbtx, hash, 1); err != nil {
			return err
		}
		if err := WriteBody(dbtx, hash, 1, body); err != nil {
			return err
		}
		return WriteReceipts(dbtx, 1, receipts)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(dbtx kv.Tx) error {
		got, err := ReadBodyWithTransactions(dbtx, hash, 1)
		if err != nil {
			return err
		}
		if got == nil {
			t.Fatal("missing stored body")
		}
		check(got.Txs)
		stored := ReadRawReceipts(dbtx, 1)
		if len(stored) != 1 || stored[0].Type != transaction.AltSigTxType || stored[0].Status != 1 || stored[0].CumulativeGasUsed != 21000 {
			t.Fatalf("receipt storage lost type/result: %+v", stored)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Existing root profiles remain distinct: Type belongs to Ethereum receipt
	// encoding, while native consensus encoding intentionally excludes it.
	var nativeAlt, nativeLegacy bytes.Buffer
	receipts.EncodeIndex(0, &nativeAlt)
	ethereumAlt := block.EthereumReceiptRoot(receipts, true)
	receipts[0].Type = transaction.LegacyTxType
	receipts.EncodeIndex(0, &nativeLegacy)
	if !bytes.Equal(nativeAlt.Bytes(), nativeLegacy.Bytes()) {
		t.Fatal("native receipt encoding changed with type")
	}
	if ethereumAlt == block.EthereumReceiptRoot(receipts, true) {
		t.Fatal("Ethereum receipt root lost type prefix")
	}
}
