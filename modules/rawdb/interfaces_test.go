// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for database access interfaces - verifying interface definitions.

package rawdb

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Interface Definition Tests
// =============================================================================

func TestChainReaderInterface(t *testing.T) {
	var _ ChainReader = (ChainReader)(nil)
	t.Log("✓ ChainReader interface is defined")
}

func TestChainWriterInterface(t *testing.T) {
	var _ ChainWriter = (ChainWriter)(nil)
	t.Log("✓ ChainWriter interface is defined")
}

func TestChainReadWriterInterface(t *testing.T) {
	var _ ChainReadWriter = (ChainReadWriter)(nil)
	t.Log("✓ ChainReadWriter interface is defined")
}

func TestReceiptReaderInterface(t *testing.T) {
	var _ ReceiptReader = (ReceiptReader)(nil)
	t.Log("✓ ReceiptReader interface is defined")
}

func TestReceiptWriterInterface(t *testing.T) {
	var _ ReceiptWriter = (ReceiptWriter)(nil)
	t.Log("✓ ReceiptWriter interface is defined")
}

func TestTxLookupReaderInterface(t *testing.T) {
	var _ TxLookupReader = (TxLookupReader)(nil)
	t.Log("✓ TxLookupReader interface is defined")
}

func TestTxLookupWriterInterface(t *testing.T) {
	var _ TxLookupWriter = (TxLookupWriter)(nil)
	t.Log("✓ TxLookupWriter interface is defined")
}

func TestHeadReaderInterface(t *testing.T) {
	var _ HeadReader = (HeadReader)(nil)
	t.Log("✓ HeadReader interface is defined")
}

func TestHeadWriterInterface(t *testing.T) {
	var _ HeadWriter = (HeadWriter)(nil)
	t.Log("✓ HeadWriter interface is defined")
}

func TestDatabaseInterface(t *testing.T) {
	var _ Database = (Database)(nil)
	t.Log("✓ Database interface is defined")
}

// =============================================================================
// Interface Composition Tests
// =============================================================================

func TestChainReadWriterComposition(t *testing.T) {
	// Verify ChainReadWriter embeds both ChainReader and ChainWriter
	var rw ChainReadWriter

	// ChainReadWriter should be usable as ChainReader
	var _ ChainReader = rw
	t.Log("✓ ChainReadWriter embeds ChainReader")

	// ChainReadWriter should be usable as ChainWriter
	var _ ChainWriter = rw
	t.Log("✓ ChainReadWriter embeds ChainWriter")
}

func TestDatabaseComposition(t *testing.T) {
	// Verify Database embeds all sub-interfaces
	var db Database

	var _ ChainReader = db
	t.Log("✓ Database embeds ChainReader")

	var _ ChainWriter = db
	t.Log("✓ Database embeds ChainWriter")

	var _ ReceiptReader = db
	t.Log("✓ Database embeds ReceiptReader")

	var _ ReceiptWriter = db
	t.Log("✓ Database embeds ReceiptWriter")

	var _ TxLookupReader = db
	t.Log("✓ Database embeds TxLookupReader")

	var _ TxLookupWriter = db
	t.Log("✓ Database embeds TxLookupWriter")

	var _ HeadReader = db
	t.Log("✓ Database embeds HeadReader")

	var _ HeadWriter = db
	t.Log("✓ Database embeds HeadWriter")
}

// =============================================================================
// Mock Implementation for Testing
// =============================================================================

// mockChainReader implements ChainReader for testing
type mockChainReader struct{}

func (m *mockChainReader) ReadCanonicalHash(number uint64) (types.Hash, error) {
	return types.Hash{}, nil
}
func (m *mockChainReader) IsCanonicalHash(hash types.Hash) (bool, error) { return false, nil }
func (m *mockChainReader) ReadHeader(hash types.Hash, number uint64) *block.Header {
	return nil
}
func (m *mockChainReader) ReadHeaderNumber(hash types.Hash) *uint64 { return nil }
func (m *mockChainReader) ReadHeaderByNumber(number uint64) *block.Header {
	return nil
}
func (m *mockChainReader) ReadHeaderByHash(hash types.Hash) (*block.Header, error) {
	return nil, nil
}
func (m *mockChainReader) HasHeader(hash types.Hash, number uint64) bool { return false }
func (m *mockChainReader) ReadBlock(hash types.Hash, number uint64) *block.Block {
	return nil
}
func (m *mockChainReader) ReadBlockByNumber(number uint64) *block.Block { return nil }
func (m *mockChainReader) ReadBlockByHash(hash types.Hash) (*block.Block, error) {
	return nil, nil
}
func (m *mockChainReader) HasBlock(hash types.Hash, number uint64) bool { return false }
func (m *mockChainReader) ReadTd(hash types.Hash, number uint64) (*uint256.Int, error) {
	return nil, nil
}

func TestMockChainReaderImplementsInterface(t *testing.T) {
	var _ ChainReader = (*mockChainReader)(nil)
	t.Log("✓ mockChainReader implements ChainReader")
}

// =============================================================================
// Interface Segregation Tests
// =============================================================================

func TestInterfaceSegregation(t *testing.T) {
	// Test that we can use minimal interfaces
	readOnlyComponent := func(r ChainReader) {
		// A component that only needs read access
		_, _ = r.ReadCanonicalHash(0)
	}

	writeOnlyComponent := func(w ChainWriter) {
		// A component that only needs write access
		_ = w.WriteCanonicalHash(types.Hash{}, 0)
	}

	// These functions accept the minimal interface needed
	_ = readOnlyComponent
	_ = writeOnlyComponent

	t.Log("✓ Interfaces follow Interface Segregation Principle")
}

