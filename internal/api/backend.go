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

// Package api provides the JSON-RPC API implementation for N42 blockchain.
//
// Backend Interface Design:
//
// The Backend interface abstracts the dependencies of the API layer, enabling:
//   - Testability: Mock implementations for unit testing
//   - Flexibility: Different backend implementations (full node, light node)
//   - Decoupling: API layer doesn't depend on concrete implementations
//
// Architecture:
//
//	┌─────────────┐
//	│   JSON-RPC  │
//	│   Handlers  │
//	└──────┬──────┘
//	       │ uses
//	       ▼
//	┌─────────────┐
//	│   Backend   │  ← Interface (this file)
//	│  Interface  │
//	└──────┬──────┘
//	       │ implements
//	       ▼
//	┌─────────────┐
//	│     API     │  ← Concrete implementation
//	│   struct    │
//	└─────────────┘
package api

import (
	"context"

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

// Backend defines the interface for API backend implementations.
// This interface abstracts all external dependencies of the API layer.
//
// Thread Safety: Implementations must be safe for concurrent access.
type Backend interface {
	// BlockchainBackend provides blockchain data access
	BlockchainBackend

	// StateBackend provides state access
	StateBackend

	// TxPoolBackend provides transaction pool access
	TxPoolBackend

	// AccountBackend provides account management
	AccountBackend

	// ConfigBackend provides configuration access
	ConfigBackend
}

// BlockchainBackend provides read-only access to blockchain data.
type BlockchainBackend interface {
	// Chain access
	BlockChain() common.IBlockChain
	Engine() consensus.Engine
	Database() kv.RwDB

	// Header access
	CurrentHeader() *block.Header
	HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*block.Header, error)
	HeaderByHash(ctx context.Context, hash types.Hash) (*block.Header, error)

	// Block access
	CurrentBlock() *block.Header
	BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*block.Block, error)
	BlockByHash(ctx context.Context, hash types.Hash) (*block.Block, error)

	// Transaction access
	GetTransaction(ctx context.Context, txHash types.Hash) (*transaction.Transaction, types.Hash, uint64, uint64, error)
	GetReceipts(ctx context.Context, blockHash types.Hash) (block.Receipts, error)

	// TD (Total Difficulty) access
	GetTd(ctx context.Context, hash types.Hash) *uint256.Int
}

// StateBackend provides access to blockchain state.
type StateBackend interface {
	// StateAtBlock returns the state at a specific block
	StateAtBlock(ctx context.Context, tx kv.Tx, blk *block.Block) (*state.IntraBlockState, error)

	// StateAtTransaction returns state at a specific transaction within a block
	StateAtTransaction(ctx context.Context, tx kv.Tx, blk *block.Block, txIndex int) (*transaction.Message, evmtypes.BlockContext, *state.IntraBlockState, error)

	// GetEVM creates a new EVM instance for message execution
	GetEVM(ctx context.Context, msg *transaction.Message, state *state.IntraBlockState, header *block.Header, vmConfig *vm.Config) (*vm.EVM, func() error, error)
}

// TxPoolBackend provides access to the transaction pool.
type TxPoolBackend interface {
	// TxsPool returns the transaction pool interface
	TxsPool() common.ITxsPool

	// SendTx submits a transaction to the pool
	// SendTx(ctx context.Context, signedTx *transaction.Transaction) error

	// GetPoolTransaction returns a transaction from the pool by hash
	// GetPoolTransaction(hash types.Hash) *transaction.Transaction

	// GetPoolNonce returns the nonce for an account in the pool
	// GetPoolNonce(ctx context.Context, addr types.Address) (uint64, error)
}

// AccountBackend provides account management access.
type AccountBackend interface {
	// AccountManager returns the account manager
	AccountManager() *accounts.Manager
}

// ConfigBackend provides configuration access.
type ConfigBackend interface {
	// ChainConfig returns the chain configuration
	ChainConfig() *params.ChainConfig

	// GetChainConfig is an alias for ChainConfig (for compatibility)
	GetChainConfig() *params.ChainConfig

	// RPCGasCap returns the gas cap for RPC calls
	// RPCGasCap() uint64

	// RPCEVMTimeout returns the EVM timeout for RPC calls
	// RPCEVMTimeout() time.Duration

	// RPCTxFeeCap returns the transaction fee cap for RPC calls
	// RPCTxFeeCap() float64
}

// Compile-time verification that API implements Backend
var _ Backend = (*API)(nil)

// =============================================================================
// Helper interfaces for specific use cases
// =============================================================================

// BlockReader provides minimal read-only access to blocks.
// Use this for components that only need to read block data.
type BlockReader interface {
	CurrentBlock() *block.Header
	BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*block.Block, error)
	BlockByHash(ctx context.Context, hash types.Hash) (*block.Block, error)
}

// HeaderReader provides minimal read-only access to headers.
// Use this for components that only need to read header data.
type HeaderReader interface {
	CurrentHeader() *block.Header
	HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*block.Header, error)
	HeaderByHash(ctx context.Context, hash types.Hash) (*block.Header, error)
}

// StateReader provides minimal state access.
// Use this for components that only need to read state.
type StateReader interface {
	StateAtBlock(ctx context.Context, tx kv.Tx, blk *block.Block) (*state.IntraBlockState, error)
}

// Compile-time verification for helper interfaces
var (
	_ BlockReader  = (*API)(nil)
	_ HeaderReader = (*API)(nil)
	_ StateReader  = (*API)(nil)
)

