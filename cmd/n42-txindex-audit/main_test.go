package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestCanonicalTxHashAtPosition(t *testing.T) {
	modules.N42Init()
	previousCodec := rawdb.CompactTxWrites
	defer func() { rawdb.CompactTxWrites = previousCodec }()
	for _, compact := range []bool{true, false} {
		name := "ethereum-rlp"
		if compact {
			name = "compact"
		}
		t.Run(name, func(t *testing.T) {
			rawdb.CompactTxWrites = compact
			db := memdb.NewTestDB(t)
			canonical := types.Hash{1}
			foreign := types.Hash{2}
			txs := []*transaction.Transaction{
				transaction.NewTransaction(1, types.Address{}, nil, uint256.NewInt(1), 21000, uint256.NewInt(1), nil),
				transaction.NewTransaction(2, types.Address{}, nil, uint256.NewInt(2), 21000, uint256.NewInt(1), nil),
			}
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				if err := rawdb.WriteCanonicalHash(tx, canonical, 7); err != nil {
					return err
				}
				if err := rawdb.WriteBodyForStorage(tx, canonical, 7, &block.BodyForStorage{BaseTxId: 40, TxAmount: 4}); err != nil {
					return err
				}
				if err := rawdb.WriteBodyForStorage(tx, foreign, 7, &block.BodyForStorage{BaseTxId: 80, TxAmount: 4}); err != nil {
					return err
				}
				return rawdb.WriteTransactions(tx, txs, 41)
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.View(context.Background(), func(tx kv.Tx) error {
				var key [8]byte
				binary.BigEndian.PutUint64(key[:], 41)
				stored, err := tx.GetOne(modules.BlockTx, key[:])
				if err != nil {
					return err
				}
				var expected []byte
				if compact {
					expected = txs[0].MarshalCompactStorage()
				} else {
					expected, err = transaction.EncodeEthereumTransaction(txs[0])
					if err != nil {
						return err
					}
				}
				if !bytes.Equal(stored, expected) {
					t.Fatal("stored transaction used a different codec")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			chain := &diskChain{db: db}
			for index, txn := range txs {
				got, err := chain.CanonicalTxHashAtPosition(7, uint64(index))
				if err != nil || got != txn.Hash() {
					t.Fatalf("position %d = %v, %v; want %v", index, got, err, txn.Hash())
				}
			}
			for _, index := range []uint64{2, math.MaxUint64} {
				if _, err := chain.CanonicalTxHashAtPosition(7, index); err == nil {
					t.Fatalf("out-of-range index %d accepted", index)
				}
			}
			if _, err := chain.CanonicalTxHashAtPosition(8, 0); err == nil {
				t.Fatal("missing canonical mapping accepted")
			}
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				var key [8]byte
				binary.BigEndian.PutUint64(key[:], 41)
				return tx.Put(modules.BlockTx, key[:], []byte{0xff})
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := chain.CanonicalTxHashAtPosition(7, 0); err == nil {
				t.Fatal("corrupt canonical transaction accepted")
			}
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				return rawdb.WriteCanonicalHash(tx, foreign, 7)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := chain.CanonicalTxHashAtPosition(7, 0); err == nil {
				t.Fatal("foreign canonical body without transaction accepted")
			}
			for _, amount := range []uint32{0, 1, 2} {
				if err := db.Update(context.Background(), func(tx kv.RwTx) error {
					return rawdb.WriteBodyForStorage(tx, foreign, 7, &block.BodyForStorage{BaseTxId: 80, TxAmount: amount})
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := chain.CanonicalTxHashAtPosition(7, 0); err == nil {
					t.Fatalf("body TxAmount %d accepted at user index 0", amount)
				}
			}
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				return rawdb.WriteBodyForStorage(tx, foreign, 7, &block.BodyForStorage{BaseTxId: math.MaxUint64, TxAmount: 4})
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := chain.CanonicalTxHashAtPosition(7, 0); err == nil {
				t.Fatal("overflowing canonical transaction range accepted")
			}
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				return tx.Put(modules.BlockBody, modules.BlockBodyKey(7, foreign), []byte{1})
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := chain.CanonicalTxHashAtPosition(7, 0); err == nil {
				t.Fatal("corrupt canonical body accepted")
			}
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				return tx.Delete(modules.BlockBody, modules.BlockBodyKey(7, foreign))
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := chain.CanonicalTxHashAtPosition(7, 0); err == nil {
				t.Fatal("missing canonical body accepted")
			}
		})
	}
}

func TestAuditOptionsArbitraryPositiveHead(t *testing.T) {
	for _, head := range []uint64{1, 25, 31, 256} {
		if err := validateAuditOptions(0, "/stopped-owner", head, "98765", 2, 1, 4075000); err != nil {
			t.Fatalf("head %d: %v", head, err)
		}
	}
	cases := []struct {
		name        string
		extra       int
		dir         string
		head        uint64
		chainID     string
		segments    int
		tail, total uint64
	}{
		{"zero head", 0, "/owner", 0, "1", 2, 1, 1},
		{"relative directory", 0, "owner", 25, "1", 2, 1, 1},
		{"missing chain ID", 0, "/owner", 25, "", 2, 1, 1},
		{"one segment", 0, "/owner", 25, "1", 1, 1, 1},
		{"no tail", 0, "/owner", 25, "1", 2, 0, 1},
		{"no exact total", 0, "/owner", 25, "1", 2, 1, 0},
		{"extra argument", 1, "/owner", 25, "1", 2, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAuditOptions(tc.extra, tc.dir, tc.head, tc.chainID, tc.segments, tc.tail, tc.total); err == nil {
				t.Fatal("invalid audit shape accepted")
			}
		})
	}
}

func TestBlockTransactionExpectation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		number    uint64
		count     int
		expected  uint64
		wantError bool
	}{
		{"strict full block", 25, 163000, 163000, false},
		{"strict partial rejected", 25, 162999, 163000, true},
		{"strict excess rejected", 25, 163001, 163000, true},
		{"strict empty rejected", 25, 0, 163000, true},
		{"genesis excluded", 0, 0, 163000, false},
		{"variable full block", 31, 163000, 0, false},
		{"variable partial block", 31, 500, 0, false},
		{"variable empty block", 31, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkBlockTransactionCount(tc.number, tc.count, tc.expected); (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError = %v", err, tc.wantError)
			}
		})
	}
}
