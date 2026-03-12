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

	internalPkg "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state/witness"
)

// WitnessAPI provides an API to retrieve block witnesses for stateless verification.
type WitnessAPI struct {
	api *API
}

// NewWitnessAPI creates a new WitnessAPI backed by the given API instance.
func NewWitnessAPI(api *API) *WitnessAPI {
	return &WitnessAPI{api: api}
}

// GetBlockWitness returns the block witness for stateless verification.
func (s *WitnessAPI) GetBlockWitness(ctx context.Context, blockNrOrHash jsonrpc.BlockNumberOrHash) (*witness.BlockWitness, error) {
	blk, err := BlockByNumberOrHash(ctx, blockNrOrHash, s.api)
	if err != nil {
		return nil, fmt.Errorf("block not found: %w", err)
	}
	if blk == nil {
		return nil, fmt.Errorf("block not found")
	}

	bc, ok := s.api.bc.(*internalPkg.BlockChain)
	if !ok {
		return nil, fmt.Errorf("witness not supported: blockchain type assertion failed")
	}

	if !bc.IsJMTEnabled() {
		return nil, fmt.Errorf("block witness not available: JMT commitment must be enabled")
	}

	w, found := bc.GetWitness(blk.Hash())
	if !found {
		return nil, fmt.Errorf("block witness not available for block %d (only recent blocks have cached witnesses; the witness may have been evicted from the LRU cache)", blk.Number64().Uint64())
	}

	return w, nil
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
