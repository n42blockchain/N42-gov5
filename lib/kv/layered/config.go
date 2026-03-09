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

package layered

import "fmt"

const (
	DefaultCacheShards   = 256
	DefaultCacheCapacity = 512 * 1024 // entries per shard (total ~128M entries)
	DefaultMaxMemoryMB   = 2048       // 2GB max cache memory
)

// Config controls the layered storage engine behavior.
type Config struct {
	// Enable activates the layered DB (two MDBX instances + cache).
	// When false, the system uses a single MDBX as before.
	Enable bool `json:"enable" yaml:"enable"`

	// StateDBPath overrides the path for the hot state DB.
	// If empty, defaults to <datadir>/statedb.
	StateDBPath string `json:"state_db_path" yaml:"state_db_path"`

	// HistoryDBPath overrides the path for the cold history DB.
	// If empty, defaults to <datadir>/historydb.
	HistoryDBPath string `json:"history_db_path" yaml:"history_db_path"`

	// CacheShards is the number of LRU cache shards (default 256).
	CacheShards int `json:"cache_shards" yaml:"cache_shards"`

	// CacheCapacity is the max entries per shard (default 512K).
	CacheCapacity int `json:"cache_capacity" yaml:"cache_capacity"`

	// MaxMemoryMB is the approximate max memory for the cache (default 2048).
	MaxMemoryMB int `json:"max_memory_mb" yaml:"max_memory_mb"`
}

// Validate checks configuration and applies defaults.
func (c *Config) Validate() error {
	if !c.Enable {
		return nil
	}

	if c.CacheShards <= 0 {
		c.CacheShards = DefaultCacheShards
	}
	if c.CacheShards > 1024 {
		return fmt.Errorf("layered: CacheShards %d exceeds max 1024", c.CacheShards)
	}
	// Must be power of 2 for fast modulo.
	if c.CacheShards&(c.CacheShards-1) != 0 {
		return fmt.Errorf("layered: CacheShards %d must be power of 2", c.CacheShards)
	}

	if c.CacheCapacity <= 0 {
		c.CacheCapacity = DefaultCacheCapacity
	}
	if c.CacheCapacity > 10_000_000 {
		return fmt.Errorf("layered: CacheCapacity %d exceeds max 10M", c.CacheCapacity)
	}

	if c.MaxMemoryMB <= 0 {
		c.MaxMemoryMB = DefaultMaxMemoryMB
	}
	if c.MaxMemoryMB > 32768 {
		return fmt.Errorf("layered: MaxMemoryMB %d exceeds max 32GB", c.MaxMemoryMB)
	}

	return nil
}
