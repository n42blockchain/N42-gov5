// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for Backend interface - verifying interface contracts.

package api

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
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

// BlockchainBackend methods
func (m *mockBackend) BlockChain() common.IBlockChain                              { return nil }
func (m *mockBackend) Engine() consensus.Engine                                    { return nil }
func (m *mockBackend) Database() kv.RwDB                                           { return nil }
func (m *mockBackend) CurrentHeader() *block.Header                                { return nil }
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

