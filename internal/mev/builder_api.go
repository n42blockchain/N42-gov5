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
//
// Local builder-API surface exposed to the proposer. Defines the
// MinerBackend interface (PendingBlock, SealedBlockValue) decoupling
// the builder API from any concrete miner, the ExecutionPayload JSON
// wire format used to reconstruct a full execution-layer block from a
// signed blinded block response, and the ErrNoPendingBlock / ErrNilRelay
// sentinel errors.

package mev

import (
	"context"
	"errors"
	"sync"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

var (
	ErrNoPendingBlock = errors.New("builder-api: no pending block available")
	ErrNilRelay       = errors.New("builder-api: relay client is nil")
)

// MinerBackend defines the interface for accessing the local block builder's
// pending block and its computed value. This decouples the builder API from
// the concrete miner implementation.
type MinerBackend interface {
	// PendingBlock returns the block currently being assembled by the local miner.
	PendingBlock() block.IBlock

	// SealedBlockValue returns the total MEV value (tips) of the locally built block.
	SealedBlockValue() *uint256.Int
}

// ExecutionPayload represents the full execution payload returned by a relay
// after submitting a signed blinded block. It contains all the data needed
// to reconstruct the complete execution-layer block.
type ExecutionPayload struct {
	ParentHash    types.Hash    `json:"parent_hash"`
	FeeRecipient  types.Address `json:"fee_recipient"`
	StateRoot     types.Hash    `json:"state_root"`
	ReceiptsRoot  types.Hash    `json:"receipts_root"`
	LogsBloom     []byte        `json:"logs_bloom"`
	BlockNumber   uint64        `json:"block_number"`
	GasLimit      uint64        `json:"gas_limit"`
	GasUsed       uint64        `json:"gas_used"`
	Timestamp     uint64        `json:"timestamp"`
	ExtraData     []byte        `json:"extra_data"`
	BaseFeePerGas *uint256.Int  `json:"base_fee_per_gas"`
	BlockHash     types.Hash    `json:"block_hash"`
	Transactions  [][]byte      `json:"transactions"`
}

// BuilderAPI provides the interface between the N42 node and the MEV-Boost
// relay infrastructure. It handles fetching bids and proposing blinded blocks.
type BuilderAPI struct {
	mu    sync.RWMutex
	relay *RelayClient
	miner MinerBackend
}

// NewBuilderAPI creates a new BuilderAPI backed by the given relay client.
// The miner backend can be set later via SetMiner if not available at construction time.
func NewBuilderAPI(relay *RelayClient) *BuilderAPI {
	return &BuilderAPI{
		relay: relay,
	}
}

// SetMiner sets the miner backend for local block value comparison.
func (api *BuilderAPI) SetMiner(miner MinerBackend) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.miner = miner
}

// ProposeBlindedBlock fetches the best bid from the relay for the specified
// slot and parent hash. The caller can then compare this bid against the
// locally built block value to decide which to use.
//
// Returns the best builder bid, or an error if no valid bid is available.
func (api *BuilderAPI) ProposeBlindedBlock(ctx context.Context, slot uint64, parentHash types.Hash) (*BuilderBid, error) {
	if api.relay == nil {
		return nil, ErrNilRelay
	}

	pubkey := api.relay.ValidatorPubkey()

	bid, err := api.relay.GetHeader(ctx, slot, parentHash, pubkey)
	if err != nil {
		return nil, err
	}

	return bid, nil
}

// LocalBlockValue returns the value of the locally built pending block.
// Returns zero if no miner backend is configured or no pending block exists.
func (api *BuilderAPI) LocalBlockValue() *uint256.Int {
	api.mu.RLock()
	miner := api.miner
	api.mu.RUnlock()

	if miner == nil {
		return new(uint256.Int)
	}
	val := miner.SealedBlockValue()
	if val == nil {
		return new(uint256.Int)
	}
	return val
}
