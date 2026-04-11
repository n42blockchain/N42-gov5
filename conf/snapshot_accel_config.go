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
// Snapshot acceleration layer configuration.
// SnapshotAccelConfig manages the in-memory diff-layer tree that
// short-circuits MDBX reads: MaxDiffLayers caps the tree depth,
// WarmupOnStart pre-populates the ShardedCache from the Account
// table and DefaultMaxDiffLayers / DefaultWarmupAccounts give a
// 128-layer / 500k-account baseline.

package conf

import "fmt"

const (
	DefaultMaxDiffLayers  = 128
	DefaultWarmupAccounts = 500_000
)

// SnapshotAccelConfig controls the snapshot acceleration layer.
// The acceleration layer maintains a tree of in-memory diff layers representing
// recent block state changes, enabling sub-millisecond reads without DB access.
type SnapshotAccelConfig struct {
	// Enable activates the snapshot acceleration layer.
	Enable bool `json:"snapshot_accel" yaml:"snapshot_accel"`

	// MaxDiffLayers is the maximum number of diff layers to keep in memory
	// before flattening the oldest into the disk-backed ShardedCache.
	MaxDiffLayers int `json:"max_diff_layers" yaml:"max_diff_layers"`

	// WarmupOnStart controls whether the ShardedCache is pre-populated
	// from MDBX Account table at startup.
	WarmupOnStart bool `json:"warmup_on_start" yaml:"warmup_on_start"`

	// WarmupAccounts is the maximum number of accounts to load during warmup.
	WarmupAccounts int `json:"warmup_accounts" yaml:"warmup_accounts"`

	// Persist enables flat snapshot persistence to MDBX tables.
	// When enabled, flattened diff layers are written to SnapshotAccount/SnapshotStorage
	// tables, and a diff layer journal is saved on shutdown for crash recovery.
	Persist bool `json:"snapshot_persist" yaml:"snapshot_persist"`
}

// Validate checks configuration values and applies defaults.
func (c *SnapshotAccelConfig) Validate() error {
	if !c.Enable {
		return nil
	}
	if c.MaxDiffLayers == 0 {
		c.MaxDiffLayers = DefaultMaxDiffLayers
	}
	if c.MaxDiffLayers < 1 || c.MaxDiffLayers > 4096 {
		return fmt.Errorf("snapshot_accel: MaxDiffLayers %d outside range [1, 4096]", c.MaxDiffLayers)
	}
	if c.WarmupAccounts == 0 {
		c.WarmupAccounts = DefaultWarmupAccounts
	}
	if c.WarmupAccounts < 0 {
		return fmt.Errorf("snapshot_accel: WarmupAccounts %d must be non-negative", c.WarmupAccounts)
	}
	return nil
}
