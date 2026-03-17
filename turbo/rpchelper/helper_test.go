package rpchelper

import (
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func init() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

func testBlockHash(number uint64) types.Hash {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], number)
	return types.BytesToHash(encoded[:])
}

func TestGetCanonicalBlockNumberUsesEarliestAvailableHistory(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		hash := testBlockHash(50)
		if err := rawdb.WriteEarliestBlock(tx, 50); err != nil {
			return err
		}
		return rawdb.WriteCanonicalHash(tx, hash, 50)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(t.Context(), func(tx kv.Tx) error {
		number, hash, err := GetCanonicalBlockNumber(jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.EarliestBlockNumber), tx)
		if err != nil {
			return err
		}
		if number == nil || number.Cmp(uint256.NewInt(50)) != 0 {
			t.Fatalf("resolved block number = %v, want 50", number)
		}
		if want := testBlockHash(50); hash != want {
			t.Fatalf("resolved hash = %s, want %s", hash, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
