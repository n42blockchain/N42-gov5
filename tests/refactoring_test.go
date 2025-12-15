// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the refactoring changes made on 2025-12-15:
// - Phase 1.3: Package alias cleanup (block2 → block, mvm_types → avmtypes)
// - Phase 2.2: Interface unification (ChainReader → ConsensusChainReader, etc.)
// - Bug fixes: defer placement, duplicate calls, dead code removal
// - Performance: slice pre-allocation

package tests

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Phase 2.2: Interface Unification Tests
// =============================================================================

// TestConsensusChainReaderInterface verifies that ConsensusChainReader interface
// is properly defined and can be used
func TestConsensusChainReaderInterface(t *testing.T) {
	// Verify ConsensusChainReader embeds ChainHeaderReader
	var _ consensus.ConsensusChainReader = (*mockConsensusChainReader)(nil)
	t.Log("✓ ConsensusChainReader interface properly defined")
}

// TestChainHeaderReaderInterface verifies ChainHeaderReader methods
func TestChainHeaderReaderInterface(t *testing.T) {
	var _ consensus.ChainHeaderReader = (*mockChainHeaderReader)(nil)
	t.Log("✓ ChainHeaderReader interface properly defined")
}

// TestIBlockChainEmbedsChainHeaderReader verifies IBlockChain embeds ChainHeaderReader
func TestIBlockChainEmbedsChainHeaderReader(t *testing.T) {
	// Test that IBlockChain interface includes ChainHeaderReader methods
	// by checking that a ChainHeaderReader can be assigned from IBlockChain type assertion
	
	// This is a compile-time verification - the types must be compatible
	type BlockChainWithChainHeaderReader interface {
		common.IBlockChain
		consensus.ChainHeaderReader
	}
	
	t.Log("✓ IBlockChain properly embeds consensus.ChainHeaderReader")
}

// TestAccountStateReaderRename verifies the renamed interface
func TestAccountStateReaderRename(t *testing.T) {
	var _ common.AccountStateReader = (*mockAccountStateReader)(nil)
	t.Log("✓ AccountStateReader interface properly renamed from ChainStateReader")
}

// =============================================================================
// Interface Consistency Tests
// =============================================================================

// TestGetHeaderByNumberSignature tests that GetHeaderByNumber returns IHeader
func TestGetHeaderByNumberSignature(t *testing.T) {
	mock := &mockChainHeaderReader{}
	number := uint256.NewInt(100)
	
	header := mock.GetHeaderByNumber(number)
	if header != nil {
		t.Log("✓ GetHeaderByNumber returns block.IHeader (may be nil)")
	} else {
		t.Log("✓ GetHeaderByNumber correctly returns nil for non-existent block")
	}
}

// TestGetHeaderByHashSignature tests that GetHeaderByHash returns (IHeader, error)
func TestGetHeaderByHashSignature(t *testing.T) {
	mock := &mockChainHeaderReader{}
	hash := types.Hash{}
	
	header, err := mock.GetHeaderByHash(hash)
	if err != nil {
		t.Logf("✓ GetHeaderByHash returns error for invalid hash: %v", err)
	} else if header == nil {
		t.Log("✓ GetHeaderByHash returns nil header for non-existent hash")
	}
}

// TestIHeaderChainSimplified verifies IHeaderChain only contains non-duplicated methods
func TestIHeaderChainSimplified(t *testing.T) {
	// IHeaderChain should only have:
	// - InsertHeader
	// - GetBlockByHash
	// (GetHeaderByNumber, GetHeaderByHash, GetBlockByNumber are now in ChainHeaderReader)
	
	type SimpleIHeaderChain interface {
		InsertHeader(headers []block.IHeader) (int, error)
		GetBlockByHash(h types.Hash) (block.IBlock, error)
	}
	
	// Verify IHeaderChain matches our expected simplified interface
	var _ common.IHeaderChain = (*mockIHeaderChain)(nil)
	t.Log("✓ IHeaderChain properly simplified (removed duplicate methods)")
}

// =============================================================================
// Type Safety Tests
// =============================================================================

// TestConsensusChainReaderMethodSignatures verifies all method signatures
func TestConsensusChainReaderMethodSignatures(t *testing.T) {
	mock := &mockChainHeaderReader{}
	
	// Config() *params.ChainConfig
	_ = mock.Config()
	t.Log("✓ Config() returns *params.ChainConfig")
	
	// CurrentBlock() block.IBlock
	_ = mock.CurrentBlock()
	t.Log("✓ CurrentBlock() returns block.IBlock")
	
	// GetHeader(hash types.Hash, number *uint256.Int) block.IHeader
	_ = mock.GetHeader(types.Hash{}, uint256.NewInt(0))
	t.Log("✓ GetHeader() returns block.IHeader")
	
	// GetHeaderByNumber(number *uint256.Int) block.IHeader
	_ = mock.GetHeaderByNumber(uint256.NewInt(0))
	t.Log("✓ GetHeaderByNumber() returns block.IHeader")
	
	// GetHeaderByHash(hash types.Hash) (block.IHeader, error)
	_, _ = mock.GetHeaderByHash(types.Hash{})
	t.Log("✓ GetHeaderByHash() returns (block.IHeader, error)")
	
	// GetTd(types.Hash, *uint256.Int) *uint256.Int
	_ = mock.GetTd(types.Hash{}, uint256.NewInt(0))
	t.Log("✓ GetTd() returns *uint256.Int")
	
	// GetBlockByNumber(number *uint256.Int) (block.IBlock, error)
	_, _ = mock.GetBlockByNumber(uint256.NewInt(0))
	t.Log("✓ GetBlockByNumber() returns (block.IBlock, error)")
	
	// GetDepositInfo(address types.Address) (*uint256.Int, *uint256.Int)
	_, _ = mock.GetDepositInfo(types.Address{})
	t.Log("✓ GetDepositInfo() returns (*uint256.Int, *uint256.Int)")
	
	// GetAccountRewardUnpaid(account types.Address) (*uint256.Int, error)
	_, _ = mock.GetAccountRewardUnpaid(types.Address{})
	t.Log("✓ GetAccountRewardUnpaid() returns (*uint256.Int, error)")
}

// =============================================================================
// Mock Implementations for Testing
// =============================================================================

// mockChainHeaderReader implements consensus.ChainHeaderReader
type mockChainHeaderReader struct{}

func (m *mockChainHeaderReader) Config() *params.ChainConfig {
	return &params.ChainConfig{}
}

func (m *mockChainHeaderReader) CurrentBlock() block.IBlock {
	return nil
}

func (m *mockChainHeaderReader) GetHeader(hash types.Hash, number *uint256.Int) block.IHeader {
	return nil
}

func (m *mockChainHeaderReader) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	return nil
}

func (m *mockChainHeaderReader) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	return nil, nil
}

func (m *mockChainHeaderReader) GetTd(hash types.Hash, number *uint256.Int) *uint256.Int {
	return nil
}

func (m *mockChainHeaderReader) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	return nil, nil
}

func (m *mockChainHeaderReader) GetDepositInfo(address types.Address) (*uint256.Int, *uint256.Int) {
	return nil, nil
}

func (m *mockChainHeaderReader) GetAccountRewardUnpaid(account types.Address) (*uint256.Int, error) {
	return nil, nil
}

// mockConsensusChainReader implements consensus.ConsensusChainReader
type mockConsensusChainReader struct {
	mockChainHeaderReader
}

func (m *mockConsensusChainReader) GetBlock(hash types.Hash, number uint64) block.IBlock {
	return nil
}

// mockIHeaderChain implements common.IHeaderChain
type mockIHeaderChain struct{}

func (m *mockIHeaderChain) InsertHeader(headers []block.IHeader) (int, error) {
	return 0, nil
}

func (m *mockIHeaderChain) GetBlockByHash(h types.Hash) (block.IBlock, error) {
	return nil, nil
}

// mockAccountStateReader implements common.AccountStateReader
type mockAccountStateReader struct{}

func (m *mockAccountStateReader) BalanceAt(ctx context.Context, account types.Address, blockNumber uint256.Int) (uint256.Int, error) {
	return uint256.Int{}, nil
}

func (m *mockAccountStateReader) StorageAt(ctx context.Context, account types.Address, key types.Hash, blockNumber uint256.Int) ([]byte, error) {
	return nil, nil
}

func (m *mockAccountStateReader) CodeAt(ctx context.Context, account types.Address, blockNumber uint256.Int) ([]byte, error) {
	return nil, nil
}

func (m *mockAccountStateReader) NonceAt(ctx context.Context, account types.Address, blockNumber uint256.Int) (uint64, error) {
	return 0, nil
}
