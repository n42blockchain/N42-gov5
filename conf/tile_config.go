// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// TileConfig configures the tile-based block processing pipeline.
// When enabled, replaces the channel-based pipeline with lock-free
// SPSC ring buffers and pinned goroutines for lower latency.
type TileConfig struct {
	// Enabled activates the tile-based pipeline. Default: false.
	Enabled bool `json:"tile_enabled" yaml:"tile_enabled"`

	// RingSize is the capacity of each SPSC ring buffer. Must be power of 2. Default: 1024.
	RingSize int `json:"tile_ring_size" yaml:"tile_ring_size"`

	// MaxRestarts is the max auto-restarts per tile before failure. Default: 3.
	MaxRestarts int `json:"tile_max_restarts" yaml:"tile_max_restarts"`

	// RestartDelayMs is the delay between tile restarts in ms. Default: 100.
	RestartDelayMs int `json:"tile_restart_delay_ms" yaml:"tile_restart_delay_ms"`

	// CPU core assignments (-1 = no pinning, Linux only).
	NetCore       int `json:"tile_net_core" yaml:"tile_net_core"`
	ConsensusCore int `json:"tile_consensus_core" yaml:"tile_consensus_core"`
	ExecuteCore   int `json:"tile_execute_core" yaml:"tile_execute_core"`
	CommitCore    int `json:"tile_commit_core" yaml:"tile_commit_core"`
	PersistCore   int `json:"tile_persist_core" yaml:"tile_persist_core"`
}

// DefaultTileConfig returns defaults with tiles disabled.
func DefaultTileConfig() TileConfig {
	return TileConfig{
		Enabled:        false,
		RingSize:       1024,
		MaxRestarts:    3,
		RestartDelayMs: 100,
		NetCore:        -1,
		ConsensusCore:  -1,
		ExecuteCore:    -1,
		CommitCore:     -1,
		PersistCore:    -1,
	}
}
