// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

// apiTNewSimulateAPI builds a BlockChainAPI/TransactionAPI pair backed by a
// memdb genesis (for state) and a matching preciseChainStub (for block
// lookups), so eth_simulateV1 and the block-by-number helpers in
// blockscout.go can be exercised end to end.
func apiTNewSimulateAPI(t *testing.T) (*BlockChainAPI, *TransactionAPI) {
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

	contract := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: cfg,
			Alloc: conf.GenesisAlloc{
				contract: {Balance: "0x56bc75e2d63100000"},
			},
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
		},
	}

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		_, _, werr := genesis.Write(tx)
		return werr
	})
	if err != nil {
		t.Fatalf("write genesis: %v", err)
	}

	stubBlock := makeTestBlock(types.Hash{}, 0, 1)
	a := &API{
		db:          db,
		engine:      &apiTestEngine{},
		chainConfig: cfg,
		bc:          newPreciseChainStub(cfg, db, stubBlock),
	}
	return NewBlockChainAPI(a), NewTransactionAPI(a, new(AddrLocker))
}

func TestTransactionAPI_GetBlockByNumber(t *testing.T) {
	_, txAPI := apiTNewSimulateAPI(t)

	blk, err := txAPI.getBlockByNumber(jsonrpc.BlockNumber(0))
	if err != nil {
		t.Fatalf("getBlockByNumber(0) error = %v", err)
	}
	if blk == nil {
		t.Fatal("getBlockByNumber(0) = nil, want genesis stub block")
	}

	blk2, err := txAPI.getBlockByNumber(jsonrpc.LatestBlockNumber)
	if err != nil {
		t.Fatalf("getBlockByNumber(latest) error = %v", err)
	}
	if blk2 == nil {
		t.Fatal("getBlockByNumber(latest) = nil")
	}

	missing, err := txAPI.getBlockByNumber(jsonrpc.BlockNumber(42))
	if err != nil {
		t.Fatalf("getBlockByNumber(42) error = %v", err)
	}
	if missing != nil {
		t.Fatalf("getBlockByNumber(42) = %v, want nil", missing)
	}
}

func TestTransactionAPI_OverlayBlockHash(t *testing.T) {
	_, txAPI := apiTNewSimulateAPI(t)

	blk, err := txAPI.getBlockByNumber(jsonrpc.BlockNumber(0))
	if err != nil || blk == nil {
		t.Fatalf("getBlockByNumber(0) = %v, %v", blk, err)
	}
	h := txAPI.overlayBlockHash(blk)
	if h == (types.Hash{}) {
		t.Fatal("overlayBlockHash() = zero hash, want non-zero")
	}

	// nil-safety paths.
	var nilAPI *TransactionAPI
	if got := nilAPI.overlayBlockHash(blk); got != (types.Hash{}) {
		t.Fatalf("overlayBlockHash(nil receiver) = %v, want zero", got)
	}
	if got := txAPI.overlayBlockHash(nil); got != (types.Hash{}) {
		t.Fatalf("overlayBlockHash(nil block) = %v, want zero", got)
	}
}

func TestTransactionAPI_GetTransactionByBlockNumberAndIndex(t *testing.T) {
	_, txAPI := apiTNewSimulateAPI(t)

	// Genesis block has no transactions, so any index is out of range and the
	// F2 fallback path (or nil, if that errors) is exercised.
	res := txAPI.GetTransactionByBlockNumberAndIndex(context.Background(), jsonrpc.BlockNumber(0), 0)
	_ = res // may be nil or an F2 fallback stub; just exercise the code path.

	// Unknown block number: F2 fallback for non-negative numbers.
	res2 := txAPI.GetTransactionByBlockNumberAndIndex(context.Background(), jsonrpc.BlockNumber(999), 0)
	_ = res2
}

func TestBlockChainAPI_SimulateV1_Empty(t *testing.T) {
	bca, _ := apiTNewSimulateAPI(t)
	res, err := bca.SimulateV1(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("SimulateV1(empty) error = %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("SimulateV1(empty) = %v, want empty", res)
	}
}

func TestBlockChainAPI_SimulateV1_TooManyBlocks(t *testing.T) {
	bca, _ := apiTNewSimulateAPI(t)
	blocks := make([]SimulateV1BlockStateCalls, maxSimulateBlocks+1)
	_, err := bca.SimulateV1(context.Background(), blocks, nil, nil)
	if err == nil {
		t.Fatal("SimulateV1(too many blocks) error = nil, want error")
	}
}

func TestBlockChainAPI_SimulateV1_TooManyCalls(t *testing.T) {
	bca, _ := apiTNewSimulateAPI(t)
	calls := make([]TransactionArgs, maxSimulateCalls+1)
	blocks := []SimulateV1BlockStateCalls{{Calls: calls}}
	_, err := bca.SimulateV1(context.Background(), blocks, nil, nil)
	if err == nil {
		t.Fatal("SimulateV1(too many calls) error = nil, want error")
	}
}

func TestBlockChainAPI_SimulateV1_BasicCall(t *testing.T) {
	bca, _ := apiTNewSimulateAPI(t)

	from := avmcommon.Address(types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	to := avmcommon.Address(types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))

	blocks := []SimulateV1BlockStateCalls{
		{
			Calls: []TransactionArgs{
				{
					From: &from,
					To:   &to,
				},
			},
		},
	}
	res, err := bca.SimulateV1(context.Background(), blocks, nil, nil)
	if err != nil {
		t.Fatalf("SimulateV1(basic call) error = %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("SimulateV1() = %d results, want 1", len(res))
	}
	if len(res[0].Calls) != 1 {
		t.Fatalf("SimulateV1() block result has %d calls, want 1", len(res[0].Calls))
	}
}

func TestBlockChainAPI_SimulateV1_BlockNotFound(t *testing.T) {
	bca, _ := apiTNewSimulateAPI(t)
	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(999))
	blocks := []SimulateV1BlockStateCalls{{}}
	_, err := bca.SimulateV1(context.Background(), blocks, &bnh, nil)
	if err == nil {
		t.Fatal("SimulateV1(unknown block) error = nil, want error")
	}
}
