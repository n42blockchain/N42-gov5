package node

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func TestWriteGenesisBlockPreservesExplicitHeaderFields(t *testing.T) {
	t.Parallel()

	cfg := &conf.Config{NodeCfg: conf.NodeConfig{DataDir: ""}}
	db, err := OpenDatabase(context.Background(), cfg, nil, kv.ChainDB.String())
	if err != nil {
		t.Fatalf("OpenDatabase() error = %v", err)
	}
	defer db.Close()

	genesis := &conf.Genesis{
		Config: &params.ChainConfig{
			LondonBlock: big.NewInt(0),
		},
		Alloc:       conf.GenesisAlloc{},
		Difficulty:  uint256.NewInt(0),
		GasLimit:    params.GenesisGasLimit,
		BaseFee:     uint256.NewInt(7),
		StateRoot:   types.HexToHash("0x95a6b74fbcb35dd5bd4dc03e03236164da625fc661cadfe58674b7cd27e664e1"),
		TxHash:      hash.EmptyRootHash,
		ReceiptHash: hash.EmptyRootHash,
	}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		_, err := WriteGenesisBlock(tx, genesis)
		return err
	}); err != nil {
		t.Fatalf("db.Update() error = %v", err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		blk, err := rawdb.ReadBlockByNumber(tx, 0)
		if err != nil {
			return err
		}
		if blk == nil {
			t.Fatal("ReadBlockByNumber(0) returned nil")
		}
		header := blk.Header()
		if header.StateRoot() != genesis.StateRoot {
			t.Fatalf("header root = %s, want %s", header.StateRoot(), genesis.StateRoot)
		}
		if blk.TxHash() != genesis.TxHash {
			t.Fatalf("header tx root = %s, want %s", blk.TxHash(), genesis.TxHash)
		}
		if concrete, ok := header.(*block.Header); ok {
			if concrete.ReceiptHash != genesis.ReceiptHash {
				t.Fatalf("header receipt root = %s, want %s", concrete.ReceiptHash, genesis.ReceiptHash)
			}
		} else {
			t.Fatalf("header type = %T, want *block.Header", header)
		}
		return nil
	}); err != nil {
		t.Fatalf("db.View() error = %v", err)
	}
}
