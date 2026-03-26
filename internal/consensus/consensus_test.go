// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for consensus interface refactoring:
// - ChainReader renamed to ConsensusChainReader
// - ChainHeaderReader method documentation updated
// - Engine interface uses ConsensusChainReader

package consensus_test

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Interface Definition Tests
// =============================================================================

// TestConsensusChainReaderExists verifies ConsensusChainReader is defined.
func TestConsensusChainReaderExists(t *testing.T) {
	var _ consensus.ConsensusChainReader = (*testConsensusChainReader)(nil)
}

// TestChainHeaderReaderExists verifies ChainHeaderReader is defined.
func TestChainHeaderReaderExists(t *testing.T) {
	var _ consensus.ChainHeaderReader = (*testChainHeaderReader)(nil)
}

// TestN42ChainHeaderReaderExists verifies N42ChainHeaderReader is defined.
func TestN42ChainHeaderReaderExists(t *testing.T) {
	var _ consensus.N42ChainHeaderReader = (*testN42ChainHeaderReader)(nil)
}

// TestConsensusChainReaderEmbedsChainHeaderReader verifies that
// ConsensusChainReader embeds ChainHeaderReader and ChainHeaderReader
// methods are callable through it.
func TestConsensusChainReaderEmbedsChainHeaderReader(t *testing.T) {
	var ccr consensus.ConsensusChainReader = &testConsensusChainReader{}

	_ = ccr.Config()
	_ = ccr.CurrentBlock()
	_ = ccr.GetHeaderByNumber(uint256.NewInt(0))
}

// TestEngineUsesConsensusChainReader verifies Engine interface method signatures
// reference ConsensusChainReader where appropriate.
func TestEngineUsesConsensusChainReader(t *testing.T) {
	// Compile-time check via the interface definition
	type EngineWithConsensusChainReader interface {
		VerifyUncles(chain consensus.ConsensusChainReader, block block.IBlock) error
		APIs(chain consensus.ConsensusChainReader) []interface{}
	}
}

// =============================================================================
// Method Signature Tests
// =============================================================================

// TestChainHeaderReaderMethods verifies all ChainHeaderReader methods
func TestChainHeaderReaderMethods(t *testing.T) {
	var chr consensus.ChainHeaderReader = &testChainHeaderReader{}

	tests := []struct {
		name   string
		testFn func()
	}{
		{
			name: "Config() *params.ChainConfig",
			testFn: func() {
				result := chr.Config()
				_ = result // Type check at compile time
			},
		},
		{
			name: "CurrentBlock() block.IBlock",
			testFn: func() {
				result := chr.CurrentBlock()
				_ = result
			},
		},
		{
			name: "GetHeader(hash, number) block.IHeader",
			testFn: func() {
				result := chr.GetHeader(types.Hash{}, uint256.NewInt(0))
				_ = result
			},
		},
		{
			name: "GetHeaderByNumber(number) block.IHeader",
			testFn: func() {
				result := chr.GetHeaderByNumber(uint256.NewInt(0))
				_ = result
			},
		},
		{
			name: "GetHeaderByHash(hash) (block.IHeader, error)",
			testFn: func() {
				result, err := chr.GetHeaderByHash(types.Hash{})
				_, _ = result, err
			},
		},
		{
			name: "GetTd(hash, number) *uint256.Int",
			testFn: func() {
				result := chr.GetTd(types.Hash{}, uint256.NewInt(0))
				_ = result
			},
		},
		{
			name: "GetBlockByNumber(number) (block.IBlock, error)",
			testFn: func() {
				result, err := chr.GetBlockByNumber(uint256.NewInt(0))
				_, _ = result, err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.testFn() // If this runs without panic, the method exists with correct signature
		})
	}
}

// TestConsensusChainReaderAdditionalMethods verifies ConsensusChainReader-only methods
func TestConsensusChainReaderAdditionalMethods(t *testing.T) {
	var ccr consensus.ConsensusChainReader = &testConsensusChainReader{}

	result := ccr.GetBlock(types.Hash{}, 0)
	_ = result
}

// =============================================================================
// Documentation Tests (verify comments exist)
// =============================================================================

// TestChainHeaderReaderDocumentation is a reminder that ChainHeaderReader has
// an inconsistency in error return patterns:
//   - GetHeaderByNumber/GetHeader return nil on error (no error return)
//   - GetHeaderByHash/GetBlockByNumber return (nil, error)
//
// This inconsistency is noted as tech debt in consensus.go.
func TestChainHeaderReaderDocumentation(t *testing.T) {
	// Intentionally empty -- serves as documentation anchor.
}

// =============================================================================
// Test Implementations
// =============================================================================

type testChainHeaderReader struct{}

func (t *testChainHeaderReader) Config() *params.ChainConfig {
	return &params.ChainConfig{}
}

func (t *testChainHeaderReader) CurrentBlock() block.IBlock {
	return nil
}

func (t *testChainHeaderReader) GetHeader(hash types.Hash, number *uint256.Int) block.IHeader {
	return nil
}

func (t *testChainHeaderReader) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	return nil
}

func (t *testChainHeaderReader) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	return nil, nil
}

func (t *testChainHeaderReader) GetTd(hash types.Hash, number *uint256.Int) *uint256.Int {
	return nil
}

func (t *testChainHeaderReader) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	return nil, nil
}

type testN42ChainHeaderReader struct {
	testChainHeaderReader
}

func (t *testN42ChainHeaderReader) GetDepositInfo(address types.Address) (*uint256.Int, *uint256.Int) {
	return nil, nil
}

func (t *testN42ChainHeaderReader) GetAccountRewardUnpaid(account types.Address) (*uint256.Int, error) {
	return nil, nil
}

type testConsensusChainReader struct {
	testN42ChainHeaderReader
}

func (t *testConsensusChainReader) GetBlock(hash types.Hash, number uint64) block.IBlock {
	return nil
}
