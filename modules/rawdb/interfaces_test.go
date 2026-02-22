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

func TestInterfaceDefinitions(t *testing.T) {
	// Verify all interfaces are well-defined by assigning nil to their types.
	// These are compile-time checks; if any interface is undefined, the test will not compile.
	var _ ChainReader = (ChainReader)(nil)
	var _ ChainWriter = (ChainWriter)(nil)
	var _ ChainReadWriter = (ChainReadWriter)(nil)
	var _ ReceiptReader = (ReceiptReader)(nil)
	var _ ReceiptWriter = (ReceiptWriter)(nil)
	var _ TxLookupReader = (TxLookupReader)(nil)
	var _ TxLookupWriter = (TxLookupWriter)(nil)
	var _ HeadReader = (HeadReader)(nil)
	var _ HeadWriter = (HeadWriter)(nil)
	var _ Database = (Database)(nil)
}

// =============================================================================
// Interface Composition Tests
// =============================================================================

func TestChainReadWriterComposition(t *testing.T) {
	var rw ChainReadWriter
	var _ ChainReader = rw
	var _ ChainWriter = rw
}

func TestDatabaseComposition(t *testing.T) {
	var db Database

	var _ ChainReader = db
	var _ ChainWriter = db
	var _ ReceiptReader = db
	var _ ReceiptWriter = db
	var _ TxLookupReader = db
	var _ TxLookupWriter = db
	var _ HeadReader = db
	var _ HeadWriter = db
}

// =============================================================================
// Mock Implementation for Testing
// =============================================================================

// mockChainReader implements ChainReader for testing.
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
}

// =============================================================================
// Interface Segregation Tests
// =============================================================================

func TestInterfaceSegregation(t *testing.T) {
	readOnlyComponent := func(r ChainReader) {
		_, _ = r.ReadCanonicalHash(0)
	}

	writeOnlyComponent := func(w ChainWriter) {
		_ = w.WriteCanonicalHash(types.Hash{}, 0)
	}

	// These functions accept the minimal interface needed
	_ = readOnlyComponent
	_ = writeOnlyComponent
}
