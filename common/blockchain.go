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

package common

import (
	"github.com/holiman/uint256"
	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// IHeaderChain provides header chain operations not covered by ChainHeaderReader.
type IHeaderChain interface {
	InsertHeader(headers []block.IHeader) (int, error)
	GetBlockByHash(h types.Hash) (block.IBlock, error)
}

// IBlockChain is the main blockchain interface.
// It embeds ChainHeaderReader (defined in common/engine.go) to ensure
// compatibility with consensus engine requirements.
//
// Note: This interface uses common layer types only, avoiding dependencies
// on internal/consensus or modules/state packages. Engine-related methods
// use interface{} to allow flexibility with different consensus implementations.
type IBlockChain interface {
	IHeaderChain
	ChainHeaderReader // Defined in common/engine.go

	Blocks() []block.IBlock
	Start() error
	GenesisBlock() block.IBlock
	NewBlockHandler(payload []byte, peer peer.ID) error
	InsertChain(blocks []block.IBlock) (int, error)
	InsertBlock(blocks []block.IBlock, isSync bool) (int, error)
	
	// SetEngine sets the consensus engine.
	// Accepts interface{} to avoid dependency on internal/consensus.
	// At runtime, this should be a consensus.Engine from internal/consensus.
	SetEngine(engine interface{})
	
	GetBlocksFromHash(hash types.Hash, n int) (blocks []block.IBlock)
	SealedBlock(b block.IBlock) error
	
	// Engine returns the consensus engine.
	// Returns interface{} to avoid dependency on internal/consensus.
	// At runtime, this returns a consensus.Engine from internal/consensus.
	Engine() interface{}
	
	GetReceipts(blockHash types.Hash) (block.Receipts, error)
	GetLogs(blockHash types.Hash) ([][]*block.Log, error)
	SetHead(head uint64) error
	AddFutureBlock(block block.IBlock) error

	// GetBlock retrieves a block by hash and number
	GetBlock(hash types.Hash, number uint64) block.IBlock

	// StateAt returns the state database at a given block number.
	// Returns interface{} to avoid dependency on modules/state.
	// At runtime, this returns a *state.IntraBlockState from modules/state.
	StateAt(tx kv.Tx, blockNr uint64) interface{}

	HasBlock(hash types.Hash, number uint64) bool

	DB() kv.RwDB
	Quit() <-chan struct{}

	Close() error

	// WriteBlockWithState writes a block with its state to the database.
	// Uses interface{} to avoid dependency on modules/state.
	// At runtime, ibs should be a *state.IntraBlockState from modules/state.
	WriteBlockWithState(block block.IBlock, receipts []*block.Receipt, ibs interface{}, nopay map[types.Address]*uint256.Int) error
}

// IMiner defines the miner interface.
type IMiner interface {
	Start()
	PendingBlockAndReceipts() (block.IBlock, block.Receipts)
}
