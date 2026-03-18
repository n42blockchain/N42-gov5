// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for Backend interface - verifying interface contracts.

package api

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	rpc "github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Interface Definition Tests
// =============================================================================

// TestBackendInterface verifies Backend interface is properly defined
func TestBackendInterface(t *testing.T) {
	// This is a compile-time check
	var _ Backend = (*API)(nil)
	t.Log("✓ Backend interface properly defined and implemented by API")
}

// TestBlockchainBackendInterface verifies BlockchainBackend interface
func TestBlockchainBackendInterface(t *testing.T) {
	var _ BlockchainBackend = (*API)(nil)
	t.Log("✓ BlockchainBackend interface properly defined")
}

// TestStateBackendInterface verifies StateBackend interface
func TestStateBackendInterface(t *testing.T) {
	var _ StateBackend = (*API)(nil)
	t.Log("✓ StateBackend interface properly defined")
}

// TestTxPoolBackendInterface verifies TxPoolBackend interface
func TestTxPoolBackendInterface(t *testing.T) {
	var _ TxPoolBackend = (*API)(nil)
	t.Log("✓ TxPoolBackend interface properly defined")
}

// TestAccountBackendInterface verifies AccountBackend interface
func TestAccountBackendInterface(t *testing.T) {
	var _ AccountBackend = (*API)(nil)
	t.Log("✓ AccountBackend interface properly defined")
}

// TestConfigBackendInterface verifies ConfigBackend interface
func TestConfigBackendInterface(t *testing.T) {
	var _ ConfigBackend = (*API)(nil)
	t.Log("✓ ConfigBackend interface properly defined")
}

// TestHelperInterfaces verifies helper interfaces
func TestHelperInterfaces(t *testing.T) {
	var _ BlockReader = (*API)(nil)
	t.Log("✓ BlockReader interface properly defined")

	var _ HeaderReader = (*API)(nil)
	t.Log("✓ HeaderReader interface properly defined")

	var _ StateReader = (*API)(nil)
	t.Log("✓ StateReader interface properly defined")
}

// =============================================================================
// Mock Backend for Testing
// =============================================================================

// mockBackend implements Backend for testing purposes
type mockBackend struct{}

type canonicalCheckChainStub struct {
	header              *block.Header
	blk                 block.IBlock
	earliest            uint64
	lastRequestedNumber uint64
	disableHeaderByHash bool
	config              *params.ChainConfig
	db                  kv.RwDB
}

func (m *canonicalCheckChainStub) Config() *params.ChainConfig { return m.config }
func (m *canonicalCheckChainStub) CurrentBlock() block.IBlock  { return m.blk }
func (m *canonicalCheckChainStub) GetHeader(hash types.Hash, number *uint256.Int) block.IHeader {
	return m.header
}
func (m *canonicalCheckChainStub) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	return m.header
}
func (m *canonicalCheckChainStub) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	if m.disableHeaderByHash {
		return nil, nil
	}
	return m.header, nil
}
func (m *canonicalCheckChainStub) GetTd(types.Hash, *uint256.Int) *uint256.Int { return nil }
func (m *canonicalCheckChainStub) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	if number != nil {
		m.lastRequestedNumber = number.Uint64()
	}
	return m.blk, nil
}
func (m *canonicalCheckChainStub) GetDepositInfo(types.Address) (*uint256.Int, *uint256.Int) {
	return nil, nil
}
func (m *canonicalCheckChainStub) GetAccountRewardUnpaid(types.Address) (*uint256.Int, error) {
	return nil, nil
}
func (m *canonicalCheckChainStub) InsertHeader(headers []block.IHeader) (int, error) { return 0, nil }
func (m *canonicalCheckChainStub) GetBlockByHash(h types.Hash) (block.IBlock, error) {
	return m.blk, nil
}
func (m *canonicalCheckChainStub) Blocks() []block.IBlock                          { return nil }
func (m *canonicalCheckChainStub) Start() error                                    { return nil }
func (m *canonicalCheckChainStub) GenesisBlock() block.IBlock                      { return nil }
func (m *canonicalCheckChainStub) NewBlockHandler(payload []byte, p peer.ID) error { return nil }
func (m *canonicalCheckChainStub) InsertChain(blocks []block.IBlock) (int, error)  { return 0, nil }
func (m *canonicalCheckChainStub) InsertBlock(blocks []block.IBlock, isSync bool) (int, error) {
	return 0, nil
}
func (m *canonicalCheckChainStub) SetEngine(engine interface{}) {}
func (m *canonicalCheckChainStub) GetBlocksFromHash(hash types.Hash, n int) []block.IBlock {
	return nil
}
func (m *canonicalCheckChainStub) SealedBlock(block.IBlock) error                       { return nil }
func (m *canonicalCheckChainStub) Engine() interface{}                                  { return nil }
func (m *canonicalCheckChainStub) GetReceipts(types.Hash) (block.Receipts, error)       { return nil, nil }
func (m *canonicalCheckChainStub) GetLogs(types.Hash) ([][]*block.Log, error)           { return nil, nil }
func (m *canonicalCheckChainStub) SetHead(head uint64) error                            { return nil }
func (m *canonicalCheckChainStub) AddFutureBlock(block.IBlock) error                    { return nil }
func (m *canonicalCheckChainStub) GetBlock(hash types.Hash, number uint64) block.IBlock { return m.blk }
func (m *canonicalCheckChainStub) StateAt(tx kv.Tx, blockNr uint64) interface{}         { return nil }
func (m *canonicalCheckChainStub) HasBlock(hash types.Hash, number uint64) bool         { return m.blk != nil }
func (m *canonicalCheckChainStub) DB() kv.RwDB                                          { return m.db }
func (m *canonicalCheckChainStub) Quit() <-chan struct{}                                { return nil }
func (m *canonicalCheckChainStub) EarliestBlock() uint64                                { return m.earliest }
func (m *canonicalCheckChainStub) Close() error                                         { return nil }
func (m *canonicalCheckChainStub) WriteBlockWithState(block.IBlock, []*block.Receipt, interface{}, map[types.Address]*uint256.Int) error {
	return nil
}

// BlockchainBackend methods
func (m *mockBackend) BlockChain() common.IBlockChain { return nil }
func (m *mockBackend) Engine() consensus.Engine       { return nil }
func (m *mockBackend) Database() kv.RwDB              { return nil }
func (m *mockBackend) CurrentHeader() *block.Header   { return nil }
func (m *mockBackend) HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*block.Header, error) {
	return nil, nil
}
func (m *mockBackend) HeaderByHash(ctx context.Context, hash types.Hash) (*block.Header, error) {
	return nil, nil
}
func (m *mockBackend) CurrentBlock() *block.Header { return nil }
func (m *mockBackend) BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*block.Block, error) {
	return nil, nil
}
func (m *mockBackend) BlockByHash(ctx context.Context, hash types.Hash) (*block.Block, error) {
	return nil, nil
}
func (m *mockBackend) GetTransaction(ctx context.Context, txHash types.Hash) (*transaction.Transaction, types.Hash, uint64, uint64, error) {
	return nil, types.Hash{}, 0, 0, nil
}
func (m *mockBackend) GetReceipts(ctx context.Context, blockHash types.Hash) (block.Receipts, error) {
	return nil, nil
}
func (m *mockBackend) GetTd(ctx context.Context, hash types.Hash) *uint256.Int { return nil }

// StateBackend methods
func (m *mockBackend) StateAtBlock(ctx context.Context, tx kv.Tx, blk *block.Block) (*state.IntraBlockState, error) {
	return nil, nil
}
func (m *mockBackend) StateAtTransaction(ctx context.Context, tx kv.Tx, blk *block.Block, txIndex int) (*transaction.Message, evmtypes.BlockContext, *state.IntraBlockState, error) {
	return nil, evmtypes.BlockContext{}, nil, nil
}
func (m *mockBackend) GetEVM(ctx context.Context, msg *transaction.Message, state *state.IntraBlockState, header *block.Header, vmConfig *vm.Config) (*vm.EVM, func() error, error) {
	return nil, nil, nil
}

// TxPoolBackend methods
func (m *mockBackend) TxsPool() common.ITxsPool { return nil }

// AccountBackend methods
func (m *mockBackend) AccountManager() *accounts.Manager { return nil }

// ConfigBackend methods
func (m *mockBackend) ChainConfig() *params.ChainConfig    { return nil }
func (m *mockBackend) GetChainConfig() *params.ChainConfig { return nil }

// TestMockBackendImplementsInterface verifies mock implements Backend
func TestMockBackendImplementsInterface(t *testing.T) {
	var _ Backend = (*mockBackend)(nil)
	t.Log("✓ mockBackend implements Backend interface")
}

// =============================================================================
// Interface Composition Tests
// =============================================================================

// TestBackendComposition verifies Backend composes sub-interfaces
func TestBackendComposition(t *testing.T) {
	var backend Backend = &mockBackend{}

	// Verify we can use backend as each sub-interface
	var _ BlockchainBackend = backend
	var _ StateBackend = backend
	var _ TxPoolBackend = backend
	var _ AccountBackend = backend
	var _ ConfigBackend = backend

	t.Log("✓ Backend properly composes all sub-interfaces")
}

// TestInterfaceGranularity verifies interface segregation principle
func TestInterfaceGranularity(t *testing.T) {
	// Components can depend on minimal interfaces
	var backend Backend = &mockBackend{}

	// A component that only needs block reading
	useBlockReader := func(br BlockReader) {
		_ = br.CurrentBlock()
	}
	useBlockReader(backend)
	t.Log("✓ BlockReader can be used independently")

	// A component that only needs header reading
	useHeaderReader := func(hr HeaderReader) {
		_ = hr.CurrentHeader()
	}
	useHeaderReader(backend)
	t.Log("✓ HeaderReader can be used independently")

	// A component that only needs state reading
	useStateReader := func(sr StateReader) {
		_, _ = sr.StateAtBlock(context.Background(), nil, nil)
	}
	useStateReader(backend)
	t.Log("✓ StateReader can be used independently")
}

func TestHeaderByNumberOrHashRequireCanonicalWithNonInternalChain(t *testing.T) {
	header := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(0)}
	api := &API{bc: &canonicalCheckChainStub{header: header}}

	args := rpc.BlockNumberOrHashWithHash(header.Hash(), true)
	_, err := api.HeaderByNumberOrHash(context.Background(), args)
	if err == nil || err.Error() != "canonical hash check unavailable for this blockchain implementation" {
		t.Fatalf("HeaderByNumberOrHash() error = %v", err)
	}
}

func TestBlockByNumberOrHashRequireCanonicalWithNonInternalChain(t *testing.T) {
	header := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(0)}
	blk := block.NewBlock(header, nil).(*block.Block)
	api := &API{bc: &canonicalCheckChainStub{header: header, blk: blk}}

	args := rpc.BlockNumberOrHashWithHash(header.Hash(), true)
	_, err := api.BlockByNumberOrHash(context.Background(), args)
	if err == nil || err.Error() != "canonical hash check unavailable for this blockchain implementation" {
		t.Fatalf("BlockByNumberOrHash() error = %v", err)
	}
}

func TestBlockByNumberLatestRejectsNilCurrentBlockNumber(t *testing.T) {
	blk := &gasPriceBlockStub{}
	api := &API{bc: &canonicalCheckChainStub{blk: blk}}

	_, err := api.BlockByNumber(context.Background(), rpc.LatestBlockNumber)
	if err == nil || err.Error() != "current block number unavailable" {
		t.Fatalf("BlockByNumber() error = %v", err)
	}
}

func TestBlockByNumberUsesEarliestAvailableAfterHistoryExpiry(t *testing.T) {
	stub := &canonicalCheckChainStub{
		blk:      &gasPriceBlockStub{header: &block.Header{Number: uint256.NewInt(42)}},
		earliest: 42,
	}
	api := &API{bc: stub}

	blk, err := BlockByNumber(context.Background(), rpc.EarliestBlockNumber, api)
	if err != nil {
		t.Fatalf("BlockByNumber() error = %v", err)
	}
	if blk == nil {
		t.Fatal("expected block, got nil")
	}
	if stub.lastRequestedNumber != 42 {
		t.Fatalf("requested block number = %d, want 42", stub.lastRequestedNumber)
	}
}

func TestBlockByNumberOrHashRejectsNilHeaderNumber(t *testing.T) {
	header := &block.Header{}
	blk := block.NewBlock(header, nil).(*block.Block)
	api := &API{bc: &canonicalCheckChainStub{header: header, blk: blk}}

	args := rpc.BlockNumberOrHashWithHash(header.Hash(), false)
	_, err := api.BlockByNumberOrHash(context.Background(), args)
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("BlockByNumberOrHash() error = %v", err)
	}
}

func TestStateAtTransactionRejectsNilBlock(t *testing.T) {
	api := &API{}

	_, _, _, err := api.StateAtTransaction(context.Background(), nil, nil, 0)
	if err == nil || err.Error() != "block is nil" {
		t.Fatalf("StateAtTransaction() error = %v", err)
	}
}

func TestStateAtBlockRejectsNilBlock(t *testing.T) {
	api := &API{}

	_, err := api.StateAtBlock(context.Background(), nil, nil)
	if err == nil || err.Error() != "block is nil" {
		t.Fatalf("StateAtBlock() error = %v", err)
	}
}
