// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package rawdb

// =============================================================================
// Database Access Interfaces
// =============================================================================
//
// This file defines interfaces for database access, establishing clear boundaries
// for different components to access blockchain data.
//
// Interface Categories:
//   - ChainReader/ChainWriter: Chain data (headers, blocks, TD)
//   - ReceiptReader/ReceiptWriter: Transaction receipts
//   - TxLookupReader/TxLookupWriter: Transaction hash lookups
//   - HeadReader/HeadWriter: Chain head management
//
// Usage Guidelines:
//   - Use the minimal interface needed for your component
//   - Prefer Reader interfaces for read-only operations
//   - Writer interfaces should be used sparingly and with care
//
// These interfaces allow:
//   - Clear separation of concerns
//   - Easy mocking for tests
//   - Dependency injection
//   - Future alternative implementations

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Chain Data Interfaces
// =============================================================================

// ChainReader provides read-only access to chain data.
// Use this interface for components that only need to read chain information.
type ChainReader interface {
	// Canonical hash access
	ReadCanonicalHash(number uint64) (types.Hash, error)
	IsCanonicalHash(hash types.Hash) (bool, error)

	// Header access
	ReadHeader(hash types.Hash, number uint64) *block.Header
	ReadHeaderNumber(hash types.Hash) *uint64
	ReadHeaderByNumber(number uint64) *block.Header
	ReadHeaderByHash(hash types.Hash) (*block.Header, error)
	HasHeader(hash types.Hash, number uint64) bool

	// Block access
	ReadBlock(hash types.Hash, number uint64) *block.Block
	ReadBlockByNumber(number uint64) *block.Block
	ReadBlockByHash(hash types.Hash) (*block.Block, error)
	HasBlock(hash types.Hash, number uint64) bool

	// Total difficulty
	ReadTd(hash types.Hash, number uint64) (*uint256.Int, error)
}

// ChainWriter provides write access to chain data.
// Use with care - modifications to chain data can cause inconsistencies.
type ChainWriter interface {
	// Canonical hash management
	WriteCanonicalHash(hash types.Hash, number uint64) error
	DeleteCanonicalHash(number uint64) error

	// Header management
	WriteHeader(header *block.Header) error
	DeleteHeader(hash types.Hash, number uint64) error

	// Block management
	WriteBlock(blk *block.Block) error
	DeleteBlock(hash types.Hash, number uint64) error

	// Total difficulty management
	WriteTd(hash types.Hash, number uint64, td *uint256.Int) error
	DeleteTd(hash types.Hash, number uint64) error
}

// ChainReadWriter combines ChainReader and ChainWriter.
type ChainReadWriter interface {
	ChainReader
	ChainWriter
}

// =============================================================================
// Receipt Interfaces
// =============================================================================

// ReceiptReader provides read-only access to transaction receipts.
type ReceiptReader interface {
	// ReadReceipts retrieves all receipts for a block by number
	ReadReceipts(number uint64) (block.Receipts, error)

	// ReadReceiptsByHash retrieves all receipts for a block by hash
	ReadReceiptsByHash(hash types.Hash) (block.Receipts, error)
}

// ReceiptWriter provides write access to transaction receipts.
type ReceiptWriter interface {
	// WriteReceipts stores receipts for a block
	WriteReceipts(number uint64, receipts block.Receipts) error

	// DeleteReceipts removes receipts for a block
	DeleteReceipts(number uint64) error
}

// ReceiptReadWriter combines ReceiptReader and ReceiptWriter.
type ReceiptReadWriter interface {
	ReceiptReader
	ReceiptWriter
}

// =============================================================================
// Transaction Lookup Interfaces
// =============================================================================

// TxLookupReader provides read access to transaction lookups.
type TxLookupReader interface {
	// ReadTxLookupEntry retrieves the block number and index for a transaction
	ReadTxLookupEntry(txHash types.Hash) (blockNumber *uint64, txIndex uint64, err error)
}

// TxLookupWriter provides write access to transaction lookups.
type TxLookupWriter interface {
	// WriteTxLookupEntries writes lookup entries for all transactions in a block
	WriteTxLookupEntries(blk *block.Block) error

	// DeleteTxLookupEntry removes a transaction lookup entry
	DeleteTxLookupEntry(txHash types.Hash) error

	// DeleteTxLookupEntries removes lookup entries for all transactions in a block
	DeleteTxLookupEntries(blk *block.Block) error
}

// TxLookupReadWriter combines TxLookupReader and TxLookupWriter.
type TxLookupReadWriter interface {
	TxLookupReader
	TxLookupWriter
}

// =============================================================================
// Head Management Interfaces
// =============================================================================

// HeadReader provides read access to chain head information.
type HeadReader interface {
	// ReadCurrentBlock retrieves the current head block
	ReadCurrentBlock() *block.Block

	// ReadCurrentHeader retrieves the current head header
	ReadCurrentHeader() *block.Header

	// ReadHeadBlockHash retrieves the hash of the current head block
	ReadHeadBlockHash() types.Hash

	// ReadHeadHeaderHash retrieves the hash of the current head header
	ReadHeadHeaderHash() types.Hash
}

// HeadWriter provides write access to chain head information.
type HeadWriter interface {
	// WriteHeadBlockHash stores the hash of the current head block
	WriteHeadBlockHash(hash types.Hash) error

	// WriteHeadHeaderHash stores the hash of the current head header
	WriteHeadHeaderHash(hash types.Hash) error
}

// HeadReadWriter combines HeadReader and HeadWriter.
type HeadReadWriter interface {
	HeadReader
	HeadWriter
}

// =============================================================================
// Combined Interfaces
// =============================================================================

// DatabaseReader combines all read interfaces.
// Use this for components that need broad read access.
type DatabaseReader interface {
	ChainReader
	ReceiptReader
	TxLookupReader
	HeadReader
}

// DatabaseWriter combines all write interfaces.
// Use with extreme care - this provides full write access.
type DatabaseWriter interface {
	ChainWriter
	ReceiptWriter
	TxLookupWriter
	HeadWriter
}

// Database combines all database interfaces.
// This is the full database access interface.
type Database interface {
	DatabaseReader
	DatabaseWriter
}

