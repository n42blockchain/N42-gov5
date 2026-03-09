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

package conf

import (
	"fmt"
	"strings"
)

// CheckpointConfig holds configuration for checkpoint sync.
// When enabled, the node fast-forwards to a trusted checkpoint block
// instead of syncing from genesis, then uses snap sync from that point.
type CheckpointConfig struct {
	Enable      bool   `json:"checkpoint_sync" yaml:"checkpoint_sync"`
	BlockNumber uint64 `json:"checkpoint_block" yaml:"checkpoint_block"`
	BlockHash   string `json:"checkpoint_hash" yaml:"checkpoint_hash"` // hex string with 0x prefix
}

// Checkpoint represents a trusted (blockNumber, blockHash) pair for a known network.
type Checkpoint struct {
	Number uint64
	Hash   string // hex string with 0x prefix
}

// Hardcoded checkpoints for known networks.
// These are populated as the chain grows and specific block hashes are verified.
var (
	MainnetCheckpoints = []Checkpoint{}
	TestnetCheckpoints = []Checkpoint{}
)

// IsEnabled returns true if checkpoint sync is explicitly enabled with valid parameters.
func (c *CheckpointConfig) IsEnabled() bool {
	return c.Enable && c.BlockNumber > 0 && c.BlockHash != ""
}

// Validate checks that configuration values are valid.
// Returns nil if disabled or if all values are acceptable.
func (c *CheckpointConfig) Validate() error {
	if !c.Enable {
		return nil
	}
	if c.BlockNumber == 0 {
		return fmt.Errorf("checkpoint sync: block number must be greater than 0")
	}
	if c.BlockHash == "" {
		return fmt.Errorf("checkpoint sync: block hash must not be empty")
	}
	hash := c.BlockHash
	if !strings.HasPrefix(hash, "0x") && !strings.HasPrefix(hash, "0X") {
		return fmt.Errorf("checkpoint sync: block hash must have 0x prefix, got %q", hash)
	}
	// A valid hash is 0x + 64 hex characters = 66 characters total.
	if len(hash) != 66 {
		return fmt.Errorf("checkpoint sync: block hash must be 66 characters (0x + 64 hex), got %d", len(hash))
	}
	return nil
}

// LatestCheckpoint returns the most recent checkpoint for the given chain name,
// or nil if no checkpoints are available.
func LatestCheckpoint(chain string) *Checkpoint {
	var checkpoints []Checkpoint
	switch chain {
	case "mainnet":
		checkpoints = MainnetCheckpoints
	case "testnet":
		checkpoints = TestnetCheckpoints
	default:
		return nil
	}
	if len(checkpoints) == 0 {
		return nil
	}
	return &checkpoints[len(checkpoints)-1]
}
