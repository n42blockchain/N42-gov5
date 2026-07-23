// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

// setupStorageTestAPI creates a BlockChainAPI backed by a memdb with a
// genesis block so that State() can resolve the latest block.
func setupStorageTestAPI(t *testing.T) *BlockChainAPI {
	t.Helper()

	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	db := memdb.NewTestDB(t)

	cfg := &params.ChainConfig{
		ChainID:               big.NewInt(1),
		Consensus:             params.Faker,
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
		ShanghaiBlock:         big.NewInt(0),
		CancunBlock:           big.NewInt(0),
	}

	// Write genesis block so that State() can resolve the latest block.
	contract := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				contract: {
					Balance: "0x0",
					// Storage entries for testing.
					Storage: map[types.Hash]types.Hash{
						types.HexToHash("0x01"): types.HexToHash("0x000000000000000000000000000000000000000000000000000000000000000a"),
						types.HexToHash("0x02"): types.HexToHash("0x000000000000000000000000000000000000000000000000000000000000001e"),
						types.HexToHash("0x03"): types.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000ff"),
					},
				},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
		},
	}

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, werr := genesis.Write(tx)
		if werr != nil {
			return werr
		}
		if blk == nil {
			return fmt.Errorf("genesis block is nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to write genesis: %v", err)
	}

	api := &API{
		db:          db,
		engine:      &apiTestEngine{},
		chainConfig: cfg,
	}
	return NewBlockChainAPI(api)
}

func TestGetStorageValues_Basic(t *testing.T) {
	bca := setupStorageTestAPI(t)

	contract := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	keys := []string{
		"0x0000000000000000000000000000000000000000000000000000000000000001",
		"0x0000000000000000000000000000000000000000000000000000000000000002",
		"0x0000000000000000000000000000000000000000000000000000000000000003",
	}

	results, err := bca.GetStorageValues(context.Background(), contract, keys, nil)
	if err != nil {
		t.Fatalf("GetStorageValues() error = %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Verify each returned value is a valid hex-encoded 32-byte value.
	for i, res := range results {
		if !strings.HasPrefix(res, "0x") {
			t.Errorf("result[%d] = %q, expected 0x prefix", i, res)
		}
		// Each result should be a 32-byte hex string (66 chars with 0x prefix).
		if len(res) != 66 {
			t.Errorf("result[%d] length = %d, expected 66", i, len(res))
		}
	}
}

func TestGetStorageAtReturnsCanonical32ByteWord(t *testing.T) {
	bca := setupStorageTestAPI(t)
	contract := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)

	got, err := bca.GetStorageAt(context.Background(), contract, "0xffff", latest)
	if err != nil {
		t.Fatalf("GetStorageAt(zero) error = %v", err)
	}
	if len(got) != 32 || !bytes.Equal(got, make([]byte, 32)) {
		t.Fatalf("GetStorageAt(zero) = %x, want 32 zero bytes", got)
	}

	got = canonicalStorageWord(uint256.NewInt(0x0a))
	if len(got) != 32 || got[31] != 0x0a {
		t.Fatalf("canonicalStorageWord(non-zero) = %x, want a 32-byte word ending in 0a", got)
	}
}

func TestGetStorageValues_TooManyKeys(t *testing.T) {
	bca := setupStorageTestAPI(t)

	contract := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Create 1025 keys (exceeds maxKeys=1024).
	keys := make([]string, 1025)
	for i := range keys {
		keys[i] = "0x0000000000000000000000000000000000000000000000000000000000000001"
	}

	_, err := bca.GetStorageValues(context.Background(), contract, keys, nil)
	if err == nil {
		t.Fatal("expected error for >1024 keys, got nil")
	}
	if !strings.Contains(err.Error(), "too many keys") {
		t.Fatalf("expected 'too many keys' error, got: %v", err)
	}
}

func TestGetStorageValues_EmptyKeys(t *testing.T) {
	bca := setupStorageTestAPI(t)

	contract := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	results, err := bca.GetStorageValues(context.Background(), contract, []string{}, nil)
	if err != nil {
		t.Fatalf("GetStorageValues() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for empty keys, got %d", len(results))
	}
}
