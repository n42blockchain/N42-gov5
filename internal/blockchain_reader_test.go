// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers BlockChain's read-only accessors (blockchain_reader.go) against a
// real memdb seeded with a genesis block and one canonical child: GetBlock*,
// GetHeader*, GetBlockNumber, GetTd, GetReceipts, and the hit/miss cache
// paths each goes through.

package internal

import (
	"context"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// newReaderTestChain builds a minimal BlockChain (no engine, no p2p) wired
// with the same LRU caches NewBlockChain constructs, over a memdb seeded
// with a genesis block (number 0) and one canonical child (number 1).
// Returns the chain plus both blocks for assertions.
func newReaderTestChain(t *testing.T) (bc *BlockChain, genesis, child *block.Block) {
	t.Helper()
	db := newRealignTestDB(t)

	genesis = block.NewBlock(&block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{0x01},
	}, nil).(*block.Block)
	from := types.HexToAddress("0x100")
	to := types.HexToAddress("0x200")
	txn := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
	child = block.NewBlock(&block.Header{
		Number:     uint256.NewInt(1),
		ParentHash: genesis.Hash(),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{0x02},
	}, []*transaction.Transaction{txn}).(*block.Block)

	receipts := block.Receipts{{
		TxHash:            txn.Hash(),
		Status:            1,
		GasUsed:           21000,
		CumulativeGasUsed: 21000,
	}}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, genesis); err != nil {
			return err
		}
		if err := rawdb.WriteBlock(tx, child); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, genesis.Hash(), 0); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, child.Hash(), 1); err != nil {
			return err
		}
		if err := rawdb.WriteTd(tx, child.Hash(), 1, uint256.NewInt(100)); err != nil {
			return err
		}
		return rawdb.WriteReceipts(tx, 1, receipts)
	}); err != nil {
		t.Fatal(err)
	}

	blockCache, _ := lru.New[types.Hash, *block.Block](8)
	tdCache, _ := lru.New[types.Hash, *uint256.Int](8)
	numberCache, _ := lru.New[types.Hash, uint64](8)
	headerCache, _ := lru.New[types.Hash, *block.Header](8)

	bc = &BlockChain{
		ChainDB:      db,
		ctx:          context.Background(),
		genesisBlock: genesis,
		blockCache:   blockCache,
		tdCache:      tdCache,
		numberCache:  numberCache,
		headerCache:  headerCache,
	}
	bc.currentBlock.Store(child)
	return bc, genesis, child
}

func TestGenesisBlockAccessor(t *testing.T) {
	bc, genesis, _ := newReaderTestChain(t)
	if got := bc.GenesisBlock(); got == nil || got.Hash() != genesis.Hash() {
		t.Fatalf("GenesisBlock() = %v, want %v", got, genesis)
	}
}

func TestGetBlockByHashAndByNumber(t *testing.T) {
	bc, genesis, child := newReaderTestChain(t)

	got, err := bc.GetBlockByHash(child.Hash())
	if err != nil || got == nil || got.Hash() != child.Hash() {
		t.Fatalf("GetBlockByHash(child) = (%v, %v), want child block", got, err)
	}
	// Second call should hit the block cache populated by the first.
	if got, err := bc.GetBlockByHash(child.Hash()); err != nil || got.Hash() != child.Hash() {
		t.Fatalf("GetBlockByHash(child) cache hit = (%v, %v)", got, err)
	}

	if _, err := bc.GetBlockByHash(types.HexToHash("0xdeadbeef")); err == nil {
		t.Fatalf("GetBlockByHash(unknown) = nil error, want errBlockDoesNotExist")
	}

	gotByNum, err := bc.GetBlockByNumber(uint256.NewInt(0))
	if err != nil || gotByNum == nil || gotByNum.Hash() != genesis.Hash() {
		t.Fatalf("GetBlockByNumber(0) = (%v, %v), want genesis", gotByNum, err)
	}

	gotMissing, err := bc.GetBlockByNumber(uint256.NewInt(99))
	if err != nil || gotMissing != nil {
		t.Fatalf("GetBlockByNumber(99) = (%v, %v), want (nil, nil)", gotMissing, err)
	}

	if got := bc.GetBlock(types.Hash{}, 0); got != nil {
		t.Fatalf("GetBlock(zero hash) = %v, want nil", got)
	}
}

func TestGetBlocksFromHashWalksToGenesis(t *testing.T) {
	bc, genesis, child := newReaderTestChain(t)

	got := bc.GetBlocksFromHash(child.Hash(), 5)
	if len(got) != 2 {
		t.Fatalf("GetBlocksFromHash() returned %d blocks, want 2 (child, genesis)", len(got))
	}
	if got[0].Hash() != child.Hash() || got[1].Hash() != genesis.Hash() {
		t.Fatalf("GetBlocksFromHash() order = [%s, %s], want [child, genesis]", got[0].Hash(), got[1].Hash())
	}

	if got := bc.GetBlocksFromHash(types.HexToHash("0xbad"), 5); got != nil {
		t.Fatalf("GetBlocksFromHash(unknown) = %v, want nil", got)
	}
}

func TestHasBlock(t *testing.T) {
	bc, genesis, _ := newReaderTestChain(t)
	if !bc.HasBlock(genesis.Hash(), 0) {
		t.Fatalf("HasBlock(genesis) = false, want true")
	}
	if bc.HasBlock(types.HexToHash("0xbad"), 0) {
		t.Fatalf("HasBlock(unknown) = true, want false")
	}
}

func TestGetHeaderAndByNumberAndByHash(t *testing.T) {
	bc, genesis, child := newReaderTestChain(t)

	h := bc.GetHeader(child.Hash(), uint256.NewInt(1))
	if h == nil || h.Hash() != child.Hash() {
		t.Fatalf("GetHeader(child) = %v, want child header", h)
	}
	// cache hit path
	if h2 := bc.GetHeader(child.Hash(), uint256.NewInt(1)); h2 == nil || h2.Hash() != child.Hash() {
		t.Fatalf("GetHeader(child) cache hit = %v", h2)
	}

	hByNum := bc.GetHeaderByNumber(uint256.NewInt(0))
	if hByNum == nil || hByNum.Hash() != genesis.Hash() {
		t.Fatalf("GetHeaderByNumber(0) = %v, want genesis header", hByNum)
	}
	if hByNum2 := bc.GetHeaderByNumber(uint256.NewInt(0)); hByNum2 == nil {
		t.Fatalf("GetHeaderByNumber(0) second call = nil")
	}
	if hMissing := bc.GetHeaderByNumber(uint256.NewInt(99)); hMissing != nil {
		t.Fatalf("GetHeaderByNumber(99) = %v, want nil", hMissing)
	}

	hByHash, err := bc.GetHeaderByHash(child.Hash())
	if err != nil || hByHash == nil || hByHash.Hash() != child.Hash() {
		t.Fatalf("GetHeaderByHash(child) = (%v, %v), want child header", hByHash, err)
	}
	hMissingByHash, err := bc.GetHeaderByHash(types.HexToHash("0xbad"))
	if err != nil || hMissingByHash != nil {
		t.Fatalf("GetHeaderByHash(unknown) = (%v, %v), want (nil, nil)", hMissingByHash, err)
	}
}

func TestGetCanonicalHashAndBlockNumber(t *testing.T) {
	bc, genesis, child := newReaderTestChain(t)

	if got := bc.GetCanonicalHash(uint256.NewInt(1)); got != child.Hash() {
		t.Fatalf("GetCanonicalHash(1) = %s, want %s", got.Hex(), child.Hash().Hex())
	}
	if got := bc.GetCanonicalHash(uint256.NewInt(99)); got != (types.Hash{}) {
		t.Fatalf("GetCanonicalHash(99) = %s, want zero hash", got.Hex())
	}

	num := bc.GetBlockNumber(child.Hash())
	if num == nil || *num != 1 {
		t.Fatalf("GetBlockNumber(child) = %v, want 1", num)
	}
	// cache hit path
	if num2 := bc.GetBlockNumber(child.Hash()); num2 == nil || *num2 != 1 {
		t.Fatalf("GetBlockNumber(child) cache hit = %v", num2)
	}
	if bc.GetBlockNumber(genesis.Hash()) == nil {
		t.Fatalf("GetBlockNumber(genesis) = nil")
	}
	if bc.GetBlockNumber(types.HexToHash("0xbad")) != nil {
		t.Fatalf("GetBlockNumber(unknown) != nil")
	}
}

func TestGetTdCacheAndLookup(t *testing.T) {
	bc, _, child := newReaderTestChain(t)

	td := bc.GetTd(child.Hash(), uint256.NewInt(1))
	if td == nil || td.Uint64() != 100 {
		t.Fatalf("GetTd(child) = %v, want 100", td)
	}
	// cache hit path
	if td2 := bc.GetTd(child.Hash(), uint256.NewInt(1)); td2 == nil || td2.Uint64() != 100 {
		t.Fatalf("GetTd(child) cache hit = %v", td2)
	}
	if td3 := bc.GetTd(types.HexToHash("0xbad"), uint256.NewInt(1)); td3 != nil {
		t.Fatalf("GetTd(unknown) = %v, want nil", td3)
	}
}

func TestGetReceiptsAndLogs(t *testing.T) {
	bc, _, child := newReaderTestChain(t)

	receipts, err := bc.GetReceipts(child.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].GasUsed != 21000 {
		t.Fatalf("GetReceipts(child) = %+v, want one receipt with GasUsed 21000", receipts)
	}

	logs, err := bc.GetLogs(child.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("GetLogs(child) = %v, want one tx's log slice", logs)
	}
}

func TestConfigEngineDBAccessors(t *testing.T) {
	bc, _, _ := newReaderTestChain(t)
	if bc.DB() == nil {
		t.Fatalf("DB() = nil")
	}
	if bc.Engine() != nil {
		t.Fatalf("Engine() = %v, want nil (no engine configured)", bc.Engine())
	}
	if bc.Config() != nil {
		t.Fatalf("Config() = %v, want nil (no chain config configured)", bc.Config())
	}
}

func TestCurrentBlockAccessor(t *testing.T) {
	bc, _, child := newReaderTestChain(t)
	if got := bc.CurrentBlock(); got == nil || got.Hash() != child.Hash() {
		t.Fatalf("CurrentBlock() = %v, want child", got)
	}
}
