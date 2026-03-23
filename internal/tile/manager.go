// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package tile

import (
	"context"
	"sync"
	"time"

	"github.com/n42blockchain/N42/log"
)

// ManagerConfig configures the TileManager.
type ManagerConfig struct {
	Enabled      bool
	RingSize     int
	MaxRestarts  int
	RestartDelay time.Duration
	CoreMap      map[TileID]int // tile → CPU core (-1 = no pinning)
}

// DefaultManagerConfig returns sensible defaults with tiles disabled.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		Enabled:      false,
		RingSize:     1024,
		MaxRestarts:  3,
		RestartDelay: 100 * time.Millisecond,
		CoreMap:      map[TileID]int{},
	}
}

// Manager creates, wires, and manages all tiles in the block processing pipeline.
//
// Pipeline topology:
//
//	NetTile ─[Ring]→ ConsensusTile ─[Ring]→ ExecuteTile ─[Ring]→ CommitTile ─[Ring]→ PersistTile
type Manager struct {
	mu    sync.RWMutex
	cfg   ManagerConfig
	tiles []*Tile
	rings []*RingBuffer

	ctx    context.Context
	cancel context.CancelFunc
}

// NewManager creates a new tile manager.
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.RingSize <= 0 {
		cfg.RingSize = 1024
	}
	return &Manager{cfg: cfg}
}

// coreFor returns the CPU core for a tile, or -1 if not configured.
func (m *Manager) coreFor(id TileID) int {
	if core, ok := m.cfg.CoreMap[id]; ok {
		return core
	}
	return -1
}

// AddTile creates and registers a tile with the pipeline.
func (m *Manager) AddTile(id TileID, fn TileFunc, inputs, outputs []*RingBuffer) *Tile {
	t := NewTile(TileConfig{
		ID:           id,
		Fn:           fn,
		Core:         m.coreFor(id),
		IO:           NewTileIO(inputs, outputs),
		MaxRestarts:  m.cfg.MaxRestarts,
		RestartDelay: m.cfg.RestartDelay,
	})
	m.mu.Lock()
	m.tiles = append(m.tiles, t)
	m.mu.Unlock()
	return t
}

// CreateRing creates a ring buffer and tracks it for cleanup.
func (m *Manager) CreateRing() *RingBuffer {
	rb := NewRingBuffer(m.cfg.RingSize)
	m.mu.Lock()
	m.rings = append(m.rings, rb)
	m.mu.Unlock()
	return rb
}

// SetupPipeline creates the standard 5-tile pipeline with ring buffers.
// The caller provides tile functions for each stage.
func (m *Manager) SetupPipeline(
	netFn TileFunc,
	consensusFn TileFunc,
	executeFn TileFunc,
	commitFn TileFunc,
	persistFn TileFunc,
) {
	// Create 4 rings connecting 5 tiles.
	netToConsensus := m.CreateRing()
	consensusToExec := m.CreateRing()
	execToCommit := m.CreateRing()
	commitToPersist := m.CreateRing()

	// Create tiles with appropriate I/O.
	m.AddTile(TileNet, netFn,
		nil, []*RingBuffer{netToConsensus})

	m.AddTile(TileConsensus, consensusFn,
		[]*RingBuffer{netToConsensus}, []*RingBuffer{consensusToExec})

	m.AddTile(TileExecute, executeFn,
		[]*RingBuffer{consensusToExec}, []*RingBuffer{execToCommit})

	m.AddTile(TileCommit, commitFn,
		[]*RingBuffer{execToCommit}, []*RingBuffer{commitToPersist})

	m.AddTile(TilePersist, persistFn,
		[]*RingBuffer{commitToPersist}, nil)
}

// Start launches all tiles.
func (m *Manager) Start(ctx context.Context) {
	m.ctx, m.cancel = context.WithCancel(ctx)

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, t := range m.tiles {
		t.Start(m.ctx)
	}

	log.Info("Tile manager started",
		"tiles", len(m.tiles),
		"rings", len(m.rings),
		"ringSize", m.cfg.RingSize,
	)
}

// Stop gracefully shuts down all tiles.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Stop in reverse order (downstream first, so producers don't write to dead consumers).
	for i := len(m.tiles) - 1; i >= 0; i-- {
		m.tiles[i].Stop()
	}

	log.Info("Tile manager stopped")
}

// Stats returns a snapshot of all tile metrics.
func (m *Manager) Stats() []TileStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make([]TileStats, len(m.tiles))
	for i, t := range m.tiles {
		stats[i] = t.Stats()
	}
	return stats
}

// RingStats returns the current occupancy of all ring buffers.
func (m *Manager) RingStats() []RingStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make([]RingStats, len(m.rings))
	for i, r := range m.rings {
		l := r.Len()
		c := r.Cap()
		stats[i] = RingStats{
			Index:     i,
			Len:       l,
			Cap:       c,
			Occupancy: float64(l) / float64(c),
		}
	}
	return stats
}

// RingStats holds a snapshot of ring buffer occupancy.
type RingStats struct {
	Index     int
	Len       int
	Cap       int
	Occupancy float64
}

// Reset drains all rings and restarts tiles for reorg recovery.
func (m *Manager) Reset(ctx context.Context) {
	// Stop all tiles.
	if m.cancel != nil {
		m.cancel()
	}

	m.mu.RLock()
	for i := len(m.tiles) - 1; i >= 0; i-- {
		m.tiles[i].Stop()
	}
	m.mu.RUnlock()

	// Drain all rings (O(1) reset since all tiles are stopped).
	m.mu.RLock()
	for _, r := range m.rings {
		r.Drain()
	}
	m.mu.RUnlock()

	// Restart.
	m.Start(ctx)
}

// TileCount returns the number of registered tiles.
func (m *Manager) TileCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tiles)
}
