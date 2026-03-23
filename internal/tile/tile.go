// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package tile

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/log"
)

// TileID uniquely identifies a tile in the pipeline.
type TileID string

const (
	TileNet       TileID = "net"
	TileConsensus TileID = "consensus"
	TileExecute   TileID = "execute"
	TileCommit    TileID = "commit"
	TilePersist   TileID = "persist"
)

// TileState represents the lifecycle state of a tile.
type TileState int32

const (
	TileCreated  TileState = iota
	TileRunning
	TileStopping
	TileStopped
	TileFailed
)

// TileFunc is the function a tile executes in its main loop.
// It receives a context for cancellation and a TileIO for ring buffer I/O.
// The function should run until ctx is cancelled, then return nil.
type TileFunc func(ctx context.Context, io *TileIO) error

// TileIO provides access to a tile's input and output ring buffers.
type TileIO struct {
	inputs  []*RingBuffer
	outputs []*RingBuffer
}

// NewTileIO creates a TileIO with the given input and output ring buffers.
func NewTileIO(inputs, outputs []*RingBuffer) *TileIO {
	return &TileIO{inputs: inputs, outputs: outputs}
}

// Recv tries to pop from the specified input ring buffer.
// Returns the item and true, or nil and false if empty.
func (io *TileIO) Recv(idx int) (any, bool) {
	if idx < 0 || idx >= len(io.inputs) {
		return nil, false
	}
	return io.inputs[idx].TryPop()
}

// Send tries to push to the specified output ring buffer.
// Returns true on success, false if the buffer is full.
func (io *TileIO) Send(idx int, item any) bool {
	if idx < 0 || idx >= len(io.outputs) {
		return false
	}
	return io.outputs[idx].TryPush(item)
}

// InputLen returns the approximate length of the specified input ring.
func (io *TileIO) InputLen(idx int) int {
	if idx < 0 || idx >= len(io.inputs) {
		return 0
	}
	return io.inputs[idx].Len()
}

// OutputLen returns the approximate length of the specified output ring.
func (io *TileIO) OutputLen(idx int) int {
	if idx < 0 || idx >= len(io.outputs) {
		return 0
	}
	return io.outputs[idx].Len()
}

// Tile is a long-lived goroutine with CPU affinity, metrics, and crash recovery.
type Tile struct {
	id   TileID
	fn   TileFunc
	core int // CPU core affinity (-1 = no affinity)
	io   *TileIO

	// Lifecycle
	state  atomic.Int32
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Crash recovery
	maxRestarts  int
	restartDelay time.Duration

	// Metrics (all atomic for lock-free reads, private to prevent external mutation)
	itemsProcessed atomic.Uint64
	lastLatencyNs  atomic.Int64
	panicsTotal    atomic.Int32
	restarts       atomic.Int32
}

// TileConfig configures a single tile.
type TileConfig struct {
	ID           TileID
	Fn           TileFunc
	Core         int           // CPU core (-1 = no pinning)
	IO           *TileIO
	MaxRestarts  int           // 0 = no restarts
	RestartDelay time.Duration
}

// NewTile creates a new tile from configuration.
func NewTile(cfg TileConfig) *Tile {
	if cfg.MaxRestarts <= 0 {
		cfg.MaxRestarts = 3
	}
	if cfg.RestartDelay <= 0 {
		cfg.RestartDelay = 100 * time.Millisecond
	}
	t := &Tile{
		id:           cfg.ID,
		fn:           cfg.Fn,
		core:         cfg.Core,
		io:           cfg.IO,
		maxRestarts:  cfg.MaxRestarts,
		restartDelay: cfg.RestartDelay,
	}
	t.state.Store(int32(TileCreated))
	return t
}

// Start launches the tile goroutine. Safe to call only once per lifecycle.
// Returns false if the tile is already running.
func (t *Tile) Start(parentCtx context.Context) bool {
	if !t.state.CompareAndSwap(int32(TileCreated), int32(TileRunning)) &&
		!t.state.CompareAndSwap(int32(TileStopped), int32(TileRunning)) &&
		!t.state.CompareAndSwap(int32(TileFailed), int32(TileRunning)) {
		return false // already running
	}
	t.ctx, t.cancel = context.WithCancel(parentCtx)
	t.wg.Add(1)
	go t.run()
	return true
}

// Stop signals the tile to shut down and waits for completion.
func (t *Tile) Stop() {
	if t.cancel != nil {
		t.cancel()
	}
	t.wg.Wait()
}

// State returns the current tile state.
func (t *Tile) State() TileState {
	return TileState(t.state.Load())
}

// ID returns the tile identifier.
func (t *Tile) ID() TileID {
	return t.id
}

// run is the main goroutine loop with CPU pinning and crash recovery.
func (t *Tile) run() {
	defer t.wg.Done()

	// Pin to CPU core if configured.
	if t.core >= 0 {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := SetCPUAffinity(t.core); err != nil {
			log.Warn("Tile: CPU affinity failed", "tile", t.id, "core", t.core, "err", err)
		} else {
			log.Debug("Tile: pinned to CPU core", "tile", t.id, "core", t.core)
		}
	}

	for {
		t.state.Store(int32(TileRunning))

		err := t.runWithRecovery()
		if err == nil || t.ctx.Err() != nil {
			t.state.Store(int32(TileStopped))
			return
		}

		// Crash recovery.
		restartCount := t.restarts.Add(1)
		t.panicsTotal.Add(1)

		if int(restartCount) > t.maxRestarts {
			t.state.Store(int32(TileFailed))
			log.Error("Tile exceeded restart limit",
				"tile", t.id, "restarts", restartCount, "lastErr", err)
			return
		}

		log.Warn("Tile crashed, restarting",
			"tile", t.id, "restarts", restartCount, "err", err)

		// Cancellable delay — don't block OS thread if Stop() is called.
		select {
		case <-time.After(t.restartDelay):
		case <-t.ctx.Done():
			t.state.Store(int32(TileStopped))
			return
		}
	}
}

// runWithRecovery calls the tile function and catches panics.
func (t *Tile) runWithRecovery() (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			retErr = fmt.Errorf("tile %s panicked: %v\n%s", t.id, r, buf[:n])
		}
	}()
	return t.fn(t.ctx, t.io)
}

// TileStats holds a snapshot of tile metrics.
type TileStats struct {
	ID             TileID
	State          TileState
	ItemsProcessed uint64
	LastLatencyNs  int64
	PanicsTotal    int32
	Restarts       int32
}

// Stats returns a snapshot of this tile's metrics.
func (t *Tile) Stats() TileStats {
	return TileStats{
		ID:             t.id,
		State:          TileState(t.state.Load()),
		ItemsProcessed: t.itemsProcessed.Load(),
		LastLatencyNs:  t.lastLatencyNs.Load(),
		PanicsTotal:    t.panicsTotal.Load(),
		Restarts:       t.restarts.Load(),
	}
}
