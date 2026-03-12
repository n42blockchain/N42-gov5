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

package api

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state/witness"
)

// WitnessAPI provides an API to retrieve block witnesses for stateless verification.
// Block witnesses contain the minimal set of Merkle proofs needed to verify
// a block's state transitions without access to the full state database.
type WitnessAPI struct {
	api *API
}

// NewWitnessAPI creates a new WitnessAPI backed by the given API instance.
func NewWitnessAPI(api *API) *WitnessAPI {
	return &WitnessAPI{api: api}
}

// GetBlockWitness returns the block witness for stateless verification.
// The witness contains all JMT Merkle proofs needed to verify the state
// transition of the given block without full state.
//
// Prerequisites:
//   - JMT commitment must be enabled on the node.
//   - The block must have been locally produced or the witness must have
//     been cached during block import.
//
// This endpoint is intended for light clients that need to verify block
// execution without downloading the entire state trie.
func (s *WitnessAPI) GetBlockWitness(ctx context.Context, blockNrOrHash jsonrpc.BlockNumberOrHash) (*witness.BlockWitness, error) {
	// Resolve the block number or hash to validate the request refers
	// to a known block.
	blk, err := BlockByNumberOrHash(ctx, blockNrOrHash, s.api)
	if err != nil {
		return nil, fmt.Errorf("block not found: %w", err)
	}
	if blk == nil {
		return nil, fmt.Errorf("block not found")
	}

	// Block witness generation requires the JMT commitment layer to be
	// active. Without it, there are no Merkle proofs to serve.
	//
	// In production, witnesses are generated during block production by
	// the miner using a TracingReader to record all state accesses, then
	// cached for serving via this RPC endpoint. The witness cache would
	// be keyed by block hash and pruned after a configurable retention
	// period.
	//
	// TODO(light-client): Wire witness cache from miner/block processor.
	// The flow would be:
	//   1. Miner wraps state reader in witness.TracingReader
	//   2. After block execution, Generator.Generate() produces BlockWitness
	//   3. BlockWitness is stored in an LRU cache keyed by block hash
	//   4. This RPC endpoint retrieves from that cache

	return nil, fmt.Errorf("block witness not available for block %d: JMT commitment must be enabled and block must be locally produced",
		blk.Number64().Uint64())
}

// APIs returns the RPC API descriptors for the witness namespace.
func (s *WitnessAPI) APIs() []jsonrpc.API {
	return []jsonrpc.API{
		{
			Namespace: "eth",
			Service:   s,
		},
	}
}
