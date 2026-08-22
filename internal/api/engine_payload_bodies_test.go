package api

import (
	"bytes"
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func makePayloadBodiesTestBlock(parentHash types.Hash, number, timestamp uint64, txs ...*transaction.Transaction) *block.Block {
	header := &block.Header{
		ParentHash: parentHash,
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(number),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       timestamp,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	blk, _ := block.NewBlock(header, txs).(*block.Block)
	return blk
}

func makePayloadBodiesTestBlockWithWithdrawalsHash(parentHash types.Hash, number, timestamp uint64, withdrawals []*Withdrawal, txs ...*transaction.Transaction) *block.Block {
	header := &block.Header{
		ParentHash: parentHash,
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(number),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       timestamp,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	root := withdrawalsRoot(withdrawals)
	header.WithdrawalsHash = &root
	blk, _ := block.NewBlock(header, txs).(*block.Block)
	return blk
}

func makePayloadBodiesTestTx(nonce uint64) *transaction.Transaction {
	to := types.HexToAddress("0x1234")
	return transaction.NewTx(&transaction.LegacyTx{
		Nonce:    nonce,
		GasPrice: uint256.NewInt(1),
		Gas:      21_000,
		To:       &to,
		Value:    uint256.NewInt(1),
	})
}

func TestGetPayloadBodiesByHashReturnsOverlayWithdrawals(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	blk := makeTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, 2)
	blkHash := ethCompatibleBlockHash(blk, cfg)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	want := &ExecutionPayloadBodyV1{
		Withdrawals: []*Withdrawal{{
			Index:          1,
			ValidatorIndex: 2,
			Address:        types.HexToAddress("0xdeadbeef"),
			Amount:         3,
		}},
	}

	api.engineOverlay.stageBlockWithBody(blk, blkHash, nil, nil, true, want)
	api.engineOverlay.importBlock(blk, blkHash, nil)

	got, err := engine.GetPayloadBodiesByHashV1(context.Background(), []types.Hash{blkHash})
	if err != nil {
		t.Fatalf("GetPayloadBodiesByHashV1() error = %v", err)
	}
	if len(got) != 1 || got[0] == nil {
		t.Fatalf("GetPayloadBodiesByHashV1() = %#v, want 1 body", got)
	}
	if len(got[0].Withdrawals) != 1 {
		t.Fatalf("len(got[0].Withdrawals) = %d, want 1", len(got[0].Withdrawals))
	}
	if *got[0].Withdrawals[0] != *want.Withdrawals[0] {
		t.Fatalf("got withdrawal = %#v, want %#v", got[0].Withdrawals[0], want.Withdrawals[0])
	}
}

func TestGetPayloadBodiesByRangeReturnsOverlayWithdrawalsAfterImport(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	blk := makeTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, 2)
	blkHash := ethCompatibleBlockHash(blk, cfg)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))
	want := &ExecutionPayloadBodyV1{
		Withdrawals: []*Withdrawal{{
			Index:          4,
			ValidatorIndex: 5,
			Address:        types.HexToAddress("0xbeef"),
			Amount:         6,
		}},
	}

	api.engineOverlay.stageBlockWithBody(blk, blkHash, nil, nil, true, want)
	api.engineOverlay.importBlock(blk, blkHash, nil)

	got, err := engine.GetPayloadBodiesByRangeV1(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("GetPayloadBodiesByRangeV1() error = %v", err)
	}
	if len(got) != 1 || got[0] == nil {
		t.Fatalf("GetPayloadBodiesByRangeV1() = %#v, want 1 body", got)
	}
	if len(got[0].Withdrawals) != 1 {
		t.Fatalf("len(got[0].Withdrawals) = %d, want 1", len(got[0].Withdrawals))
	}
	if *got[0].Withdrawals[0] != *want.Withdrawals[0] {
		t.Fatalf("got withdrawal = %#v, want %#v", got[0].Withdrawals[0], want.Withdrawals[0])
	}
}

func TestGetPayloadBodiesPreservesExplicitEmptyWithdrawals(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	blk := makeTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, 2)
	blkHash := ethCompatibleBlockHash(blk, cfg)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))

	api.engineOverlay.stageBlockWithBody(blk, blkHash, nil, nil, true, &ExecutionPayloadBodyV1{
		Withdrawals: []*Withdrawal{},
	})
	api.engineOverlay.importBlock(blk, blkHash, nil)

	got, err := engine.GetPayloadBodiesByHashV1(context.Background(), []types.Hash{blkHash})
	if err != nil {
		t.Fatalf("GetPayloadBodiesByHashV1() error = %v", err)
	}
	if len(got) != 1 || got[0] == nil {
		t.Fatalf("GetPayloadBodiesByHashV1() = %#v, want 1 body", got)
	}
	if got[0].Withdrawals == nil {
		t.Fatal("GetPayloadBodiesByHashV1() withdrawals = nil, want empty slice")
	}
	if len(got[0].Withdrawals) != 0 {
		t.Fatalf("len(GetPayloadBodiesByHashV1().withdrawals) = %d, want 0", len(got[0].Withdrawals))
	}
}

func TestGetPayloadBodiesFallbackPreservesShanghaiEmptyWithdrawals(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makePayloadBodiesTestBlock(types.Hash{}, 0, 1)
	blk := makePayloadBodiesTestBlockWithWithdrawalsHash(ethCompatibleBlockHash(genesis, cfg), 1, 2, []*Withdrawal{})
	blkHash := ethCompatibleBlockHash(blk, cfg)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis, blk),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))

	got, err := engine.GetPayloadBodiesByHashV1(context.Background(), []types.Hash{blkHash})
	if err != nil {
		t.Fatalf("GetPayloadBodiesByHashV1() error = %v", err)
	}
	if len(got) != 1 || got[0] == nil {
		t.Fatalf("GetPayloadBodiesByHashV1() = %#v, want 1 body", got)
	}
	if got[0].Withdrawals == nil {
		t.Fatal("GetPayloadBodiesByHashV1() withdrawals = nil, want empty slice")
	}
	if len(got[0].Withdrawals) != 0 {
		t.Fatalf("len(GetPayloadBodiesByHashV1().withdrawals) = %d, want 0", len(got[0].Withdrawals))
	}
}

func TestGetPayloadBodiesFallBackToCanonicalChain(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makePayloadBodiesTestBlock(types.Hash{}, 0, 1)
	tx := makePayloadBodiesTestTx(1)
	blk := makePayloadBodiesTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, 2, tx)
	blkHash := ethCompatibleBlockHash(blk, cfg)
	wantTxs, _, err := encodeEthereumTransactions([]*transaction.Transaction{tx})
	if err != nil {
		t.Fatalf("encodeEthereumTransactions() error = %v", err)
	}
	wantTx := wantTxs[0]

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis, blk),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))

	byHash, err := engine.GetPayloadBodiesByHashV1(context.Background(), []types.Hash{blkHash})
	if err != nil {
		t.Fatalf("GetPayloadBodiesByHashV1() error = %v", err)
	}
	if len(byHash) != 1 || byHash[0] == nil || len(byHash[0].Transactions) != 1 {
		t.Fatalf("GetPayloadBodiesByHashV1() = %#v, want 1 transaction", byHash)
	}
	if !bytes.Equal(byHash[0].Transactions[0], wantTx) {
		t.Fatalf("hash body tx = %x, want %x", []byte(byHash[0].Transactions[0]), wantTx)
	}
	if len(byHash[0].Withdrawals) != 0 {
		t.Fatalf("len(hash body withdrawals) = %d, want 0", len(byHash[0].Withdrawals))
	}

	byRange, err := engine.GetPayloadBodiesByRangeV1(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("GetPayloadBodiesByRangeV1() error = %v", err)
	}
	if len(byRange) != 1 || byRange[0] == nil || len(byRange[0].Transactions) != 1 {
		t.Fatalf("GetPayloadBodiesByRangeV1() = %#v, want 1 transaction", byRange)
	}
	if !bytes.Equal(byRange[0].Transactions[0], wantTx) {
		t.Fatalf("range body tx = %x, want %x", []byte(byRange[0].Transactions[0]), wantTx)
	}
	if len(byRange[0].Withdrawals) != 0 {
		t.Fatalf("len(range body withdrawals) = %d, want 0", len(byRange[0].Withdrawals))
	}
}

func TestGetPayloadBodiesByRangeTruncatesAtCanonicalHead(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makePayloadBodiesTestBlock(types.Hash{}, 0, 1)
	blk := makePayloadBodiesTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, 2)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis, blk),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))

	got, err := engine.GetPayloadBodiesByRangeV1(context.Background(), 2, 1)
	if err != nil {
		t.Fatalf("GetPayloadBodiesByRangeV1() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(GetPayloadBodiesByRangeV1(2,1)) = %d, want 0", len(got))
	}

	got, err = engine.GetPayloadBodiesByRangeV1(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("GetPayloadBodiesByRangeV1() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(GetPayloadBodiesByRangeV1(1,2)) = %d, want 1", len(got))
	}
}

func TestGetPayloadBodiesByRangeRejectsInvalidParams(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makePayloadBodiesTestBlock(types.Hash{}, 0, 1)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIv4(NewBlockChainAPI(api))

	tests := []struct {
		name      string
		start     hexutil.Uint64
		count     hexutil.Uint64
		errorCode int
	}{
		{
			name:      "zero start",
			start:     0,
			count:     1,
			errorCode: -32602,
		},
		{
			name:      "zero count",
			start:     1,
			count:     0,
			errorCode: -32602,
		},
		{
			name:      "count too large",
			start:     1,
			count:     1025,
			errorCode: -38004,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := engine.GetPayloadBodiesByRangeV1(context.Background(), tc.start, tc.count)
			if err == nil {
				t.Fatalf("GetPayloadBodiesByRangeV1(%d,%d) error = nil, got %#v", tc.start, tc.count, got)
			}
			coded, ok := err.(interface{ ErrorCode() int })
			if !ok {
				t.Fatalf("GetPayloadBodiesByRangeV1(%d,%d) error lacks ErrorCode(): %T", tc.start, tc.count, err)
			}
			if coded.ErrorCode() != tc.errorCode {
				t.Fatalf("GetPayloadBodiesByRangeV1(%d,%d) ErrorCode() = %d, want %d", tc.start, tc.count, coded.ErrorCode(), tc.errorCode)
			}
		})
	}
}

func TestGetPayloadBodiesByHashRejectsTooManyHashes(t *testing.T) {
	engine := NewEngineAPIv4(NewBlockChainAPI(&API{engineOverlay: newEngineOverlay()}))

	got, err := engine.GetPayloadBodiesByHashV1(context.Background(), make([]types.Hash, 33))
	if err == nil {
		t.Fatalf("GetPayloadBodiesByHashV1() error = nil, got %#v", got)
	}
	coded, ok := err.(interface{ ErrorCode() int })
	if !ok {
		t.Fatalf("GetPayloadBodiesByHashV1() error lacks ErrorCode(): %T", err)
	}
	if coded.ErrorCode() != -38004 {
		t.Fatalf("GetPayloadBodiesByHashV1() ErrorCode() = %d, want -38004", coded.ErrorCode())
	}

	got, err = engine.GetPayloadBodiesByHashV1(context.Background(), make([]types.Hash, 32))
	if err != nil {
		t.Fatalf("GetPayloadBodiesByHashV1(32): %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("len(GetPayloadBodiesByHashV1(32)) = %d, want 32", len(got))
	}
}

func TestGetBlobsV1RejectsTooManyHashes(t *testing.T) {
	engine := NewEngineAPIv4(nil)

	got, err := engine.GetBlobsV1(context.Background(), make([]types.Hash, 129))
	if err == nil {
		t.Fatalf("GetBlobsV1() error = nil, got %#v", got)
	}
	coded, ok := err.(interface{ ErrorCode() int })
	if !ok {
		t.Fatalf("GetBlobsV1() error lacks ErrorCode(): %T", err)
	}
	if coded.ErrorCode() != -38004 {
		t.Fatalf("GetBlobsV1() ErrorCode() = %d, want -38004", coded.ErrorCode())
	}

	got, err = engine.GetBlobsV1(context.Background(), make([]types.Hash, 128))
	if err != nil {
		t.Fatalf("GetBlobsV1(128): %v", err)
	}
	if len(got) != 128 {
		t.Fatalf("len(GetBlobsV1(128)) = %d, want 128", len(got))
	}
}
