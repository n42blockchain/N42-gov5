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

package mcp

import (
	"context"
	"fmt"
)

// registerDefaultResources registers all built-in chain state resources.
func (s *Server) registerDefaultResources() {
	s.RegisterResource(ResourceProvider{
		Name:        "chainId",
		Description: "Current chain ID",
		Handler:     s.resourceChainId,
	})

	s.RegisterResource(ResourceProvider{
		Name:        "latestBlock",
		Description: "Latest block number and hash",
		Handler:     s.resourceLatestBlock,
	})

	s.RegisterResource(ResourceProvider{
		Name:        "syncStatus",
		Description: "Node synchronization status",
		Handler:     s.resourceSyncStatus,
	})

	s.RegisterResource(ResourceProvider{
		Name:        "peerCount",
		Description: "Number of connected peers",
		Handler:     s.resourcePeerCount,
	})
}

// resourceChainId returns the current chain ID.
func (s *Server) resourceChainId(_ context.Context) (interface{}, error) {
	bc := s.backend.BlockChain()
	cfg := bc.Config()
	if cfg == nil || cfg.ChainID == nil {
		return nil, fmt.Errorf("chain config not available")
	}

	return map[string]interface{}{
		"chain_id":   cfg.ChainID.Uint64(),
		"chain_name": cfg.ChainName,
	}, nil
}

// resourceLatestBlock returns the latest block number and hash.
func (s *Server) resourceLatestBlock(_ context.Context) (interface{}, error) {
	bc := s.backend.BlockChain()
	current := bc.CurrentBlock()
	if current == nil {
		return nil, fmt.Errorf("no current block available")
	}

	return map[string]interface{}{
		"number":    current.Number64().Uint64(),
		"hash":      current.Hash().Hex(),
		"timestamp": current.Time(),
		"gas_used":  current.GasUsed(),
		"gas_limit": current.GasLimit(),
	}, nil
}

// resourceSyncStatus returns the node synchronization status.
func (s *Server) resourceSyncStatus(_ context.Context) (interface{}, error) {
	status := s.backend.SyncProgress()
	if status == nil {
		// Node is not syncing — report current state.
		bc := s.backend.BlockChain()
		current := bc.CurrentBlock()
		if current == nil {
			return map[string]interface{}{
				"syncing":       false,
				"current_block": 0,
				"highest_block": 0,
			}, nil
		}
		return map[string]interface{}{
			"syncing":       false,
			"current_block": current.Number64().Uint64(),
			"highest_block": current.Number64().Uint64(),
		}, nil
	}

	return map[string]interface{}{
		"syncing":       status.Syncing,
		"current_block": status.CurrentBlock,
		"highest_block": status.HighestBlock,
	}, nil
}

// resourcePeerCount returns the number of connected peers.
func (s *Server) resourcePeerCount(_ context.Context) (interface{}, error) {
	return map[string]interface{}{
		"peer_count": s.backend.PeerCount(),
	}, nil
}
