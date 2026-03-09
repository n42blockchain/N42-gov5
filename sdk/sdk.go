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

// Package sdk provides a thin public API for using N42 as a library.
// It re-exports core types and offers convenience functions for common
// operations such as opening databases and reading block data.
package sdk

import (
	"context"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// -----------------------------------------------------------------------
// Type aliases for convenience
// -----------------------------------------------------------------------

type (
	// Address is a 20-byte N42/Ethereum address.
	Address = types.Address
	// Hash is a 32-byte Keccak-256 hash.
	Hash = types.Hash
	// Block is the interface for a complete block (header + body).
	Block = block.IBlock
	// Header is the interface for a block header.
	Header = block.IHeader
	// Transaction is a signed Ethereum transaction.
	Transaction = transaction.Transaction
	// Receipt contains the results of a transaction execution.
	Receipt = block.Receipt
	// Log is a contract log event.
	Log = block.Log
	// DB is a read-write key-value database.
	DB = kv.RwDB
	// Tx is a read-only database transaction.
	Tx = kv.Tx
	// RwTx is a read-write database transaction.
	RwTx = kv.RwTx
	// ChainConfig holds chain-specific configuration parameters.
	ChainConfig = params.ChainConfig
	// FreezerAPI is the interface for ancient/cold block storage.
	FreezerAPI = freezer.FreezerAPI
)

// -----------------------------------------------------------------------
// Database helpers
// -----------------------------------------------------------------------

// OpenDB opens an MDBX database at the given path with the specified label.
// The context controls the lifetime of background DB operations.
func OpenDB(ctx context.Context, path string, label kv.Label) (DB, error) {
	return mdbx.NewMDBX(nil).Path(path).Label(label).Open(ctx)
}

// NewMemDB creates an in-memory database suitable for testing.
func NewMemDB() DB {
	return memdb.New("")
}

// -----------------------------------------------------------------------
// Block data readers
// -----------------------------------------------------------------------

// ReadBlock reads a block from the database by hash and number.
// Returns nil if the block is not found.
func ReadBlock(tx Tx, hash Hash, number uint64) *block.Block {
	return rawdb.ReadBlock(tx, hash, number)
}

// ReadReceipts reads receipts for a block from the database.
// The senders slice is used to derive sender addresses in receipt fields.
func ReadReceipts(tx Tx, b *block.Block, senders []Address) []*Receipt {
	return rawdb.ReadReceipts(tx, b, senders)
}

// ReadCurrentBlock reads the current canonical head block from the database.
func ReadCurrentBlock(tx Tx) *block.Block {
	return rawdb.ReadCurrentBlock(tx)
}

// ReadCanonicalHash reads the canonical block hash for a given block number.
func ReadCanonicalHash(tx Tx, number uint64) (Hash, error) {
	return rawdb.ReadCanonicalHash(tx, number)
}

// ReadChainConfig reads the chain configuration stored at the given genesis hash.
func ReadChainConfig(tx Tx, genesisHash Hash) (*ChainConfig, error) {
	return rawdb.ReadChainConfig(tx, genesisHash)
}

// -----------------------------------------------------------------------
// Chain parameter helpers
// -----------------------------------------------------------------------

// MainnetChainConfig returns the chain configuration for the N42 mainnet.
func MainnetChainConfig() *ChainConfig {
	return params.MainnetChainConfig
}

// TestnetChainConfig returns the chain configuration for the N42 testnet.
func TestnetChainConfig() *ChainConfig {
	return params.TestnetChainConfig
}

// -----------------------------------------------------------------------
// Label constants (re-exported for convenience)
// -----------------------------------------------------------------------

const (
	LabelChainDB = kv.ChainDB
)
