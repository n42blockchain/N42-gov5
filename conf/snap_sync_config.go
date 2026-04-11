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
// Snap sync configuration.
// SnapSyncConfig tunes the state sync protocol: PivotDistance,
// MaxConcurrency, MaxBytesPerReq, MinSnapPeers and the SyncThreshold
// in blocks behind HEAD that triggers snap mode. Default constants
// DefaultSnap* give a conservative 64-pivot / 16-concurrency
// envelope suitable for mainnet bootstrapping.

package conf

import "fmt"

const (
	DefaultSnapPivotDistance   = 64
	DefaultSnapMaxConcurrency = 16
	DefaultSnapMaxBytesPerReq = 512 * 1024 // 512KB
	DefaultSnapMinPeers       = 3
	DefaultSnapSyncThreshold  = 1000 // blocks behind to trigger snap sync
)

// SnapSyncConfig holds configuration for snap sync.
type SnapSyncConfig struct {
	Enable         bool   `json:"enable" yaml:"enable"`
	PivotDistance   uint64 `json:"pivot_distance" yaml:"pivot_distance"`
	MaxConcurrency int    `json:"max_concurrency" yaml:"max_concurrency"`
	MaxBytesPerReq uint64 `json:"max_bytes_per_req" yaml:"max_bytes_per_req"`
	MinSnapPeers   int    `json:"min_snap_peers" yaml:"min_snap_peers"`
	SyncThreshold  uint64 `json:"sync_threshold" yaml:"sync_threshold"`
}

// IsEnabled returns true if snap sync is explicitly enabled.
func (c *SnapSyncConfig) IsEnabled() bool {
	return c.Enable
}

// Validate checks that configuration values are within acceptable ranges
// and applies defaults where needed. Returns an error for invalid values.
func (c *SnapSyncConfig) Validate() error {
	if !c.Enable {
		return nil
	}
	if c.PivotDistance == 0 {
		c.PivotDistance = DefaultSnapPivotDistance
	}
	if c.PivotDistance > 256 {
		return fmt.Errorf("snap sync: PivotDistance %d exceeds max 256", c.PivotDistance)
	}
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = DefaultSnapMaxConcurrency
	}
	if c.MaxConcurrency > 64 {
		return fmt.Errorf("snap sync: MaxConcurrency %d exceeds max 64", c.MaxConcurrency)
	}
	if c.MaxBytesPerReq == 0 {
		c.MaxBytesPerReq = DefaultSnapMaxBytesPerReq
	}
	if c.MaxBytesPerReq < 64*1024 {
		return fmt.Errorf("snap sync: MaxBytesPerReq %d below minimum 64KB", c.MaxBytesPerReq)
	}
	if c.MaxBytesPerReq > 8*1024*1024 {
		return fmt.Errorf("snap sync: MaxBytesPerReq %d exceeds max 8MB", c.MaxBytesPerReq)
	}
	if c.MinSnapPeers == 0 {
		c.MinSnapPeers = DefaultSnapMinPeers
	}
	if c.MinSnapPeers > 32 {
		return fmt.Errorf("snap sync: MinSnapPeers %d exceeds max 32", c.MinSnapPeers)
	}
	return nil
}
