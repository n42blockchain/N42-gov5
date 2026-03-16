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

package sync

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	nodeMetrics "github.com/n42blockchain/N42/internal/metrics"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/log"
)

// SyncState represents the current synchronization state of the node.
type SyncState int32

const (
	// SyncStateIdle indicates the node is not actively syncing.
	// This is the initial state or when no sync is needed.
	SyncStateIdle SyncState = iota

	// SyncStateInitialSync indicates the node is performing initial synchronization.
	// This happens when the node is far behind the network head.
	SyncStateInitialSync

	// SyncStateCatchUp indicates the node is catching up to the network head.
	// This happens when the node is slightly behind after initial sync or reconnection.
	SyncStateCatchUp

	// SyncStateSynced indicates the node is fully synchronized with the network.
	SyncStateSynced
)

// String returns the string representation of a SyncState.
func (s SyncState) String() string {
	switch s {
	case SyncStateIdle:
		return "Idle"
	case SyncStateInitialSync:
		return "InitialSync"
	case SyncStateCatchUp:
		return "CatchUp"
	case SyncStateSynced:
		return "Synced"
	default:
		return fmt.Sprintf("Unknown(%d)", s)
	}
}

// SyncMetrics collects synchronization metrics.
type SyncMetrics struct {
	mu sync.RWMutex

	// State duration tracking
	stateEnterTime map[SyncState]time.Time
	stateDuration  map[SyncState]time.Duration

	// Block metrics
	blocksProcessed uint64
	blocksFailed    uint64
	lastBlockTime   time.Time

	// Connection metrics
	disconnectCount    uint64
	reconnectDurations []time.Duration
}

// NewSyncMetrics creates a new SyncMetrics instance.
func NewSyncMetrics() *SyncMetrics {
	return &SyncMetrics{
		stateEnterTime: make(map[SyncState]time.Time),
		stateDuration:  make(map[SyncState]time.Duration),
	}
}

// EnterState records entering a new state.
func (m *SyncMetrics) EnterState(state SyncState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateEnterTime[state] = time.Now()
}

// ExitState records exiting a state and accumulates duration.
func (m *SyncMetrics) ExitState(state SyncState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if enterTime, ok := m.stateEnterTime[state]; ok {
		m.stateDuration[state] += time.Since(enterTime)
	}
}

// RecordBlocksProcessed increments the processed blocks counter.
func (m *SyncMetrics) RecordBlocksProcessed(count uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blocksProcessed += count
	m.lastBlockTime = time.Now()
}

// RecordBlocksFailed increments the failed blocks counter.
func (m *SyncMetrics) RecordBlocksFailed(count uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blocksFailed += count
}

// RecordDisconnect records a disconnection event.
func (m *SyncMetrics) RecordDisconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnectCount++
}

// RecordReconnect records a reconnection duration.
func (m *SyncMetrics) RecordReconnect(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconnectDurations = append(m.reconnectDurations, duration)
}

// BlocksPerSecond returns the current blocks/second rate.
func (m *SyncMetrics) BlocksPerSecond() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.lastBlockTime.IsZero() {
		return 0
	}
	elapsed := time.Since(m.lastBlockTime).Seconds()
	if elapsed < 1 {
		elapsed = 1
	}
	return float64(m.blocksProcessed) / elapsed
}

// FailureRate returns the block failure rate.
func (m *SyncMetrics) FailureRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	total := m.blocksProcessed + m.blocksFailed
	if total == 0 {
		return 0
	}
	return float64(m.blocksFailed) / float64(total)
}

// StateDuration returns the total time spent in a given state.
func (m *SyncMetrics) StateDuration(state SyncState) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stateDuration[state]
}

// LogStats logs the current metrics.
func (m *SyncMetrics) LogStats() {
	m.mu.RLock()
	// Copy values while holding lock to avoid nested lock issues
	blocksProcessed := m.blocksProcessed
	blocksFailed := m.blocksFailed
	disconnectCount := m.disconnectCount
	idleTime := m.stateDuration[SyncStateIdle]
	initialSyncTime := m.stateDuration[SyncStateInitialSync]
	catchupTime := m.stateDuration[SyncStateCatchUp]
	syncedTime := m.stateDuration[SyncStateSynced]

	// Calculate failure rate inline to avoid nested lock
	var failureRate float64
	total := blocksProcessed + blocksFailed
	if total > 0 {
		failureRate = float64(blocksFailed) / float64(total)
	}
	m.mu.RUnlock()

	log.Info("Sync metrics",
		"blocks_processed", blocksProcessed,
		"blocks_failed", blocksFailed,
		"failure_rate", fmt.Sprintf("%.2f%%", failureRate*100),
		"disconnects", disconnectCount,
		"idle_time", idleTime,
		"initial_sync_time", initialSyncTime,
		"catchup_time", catchupTime,
		"synced_time", syncedTime,
	)
}

// SyncStateMachineConfig holds configuration for the state machine.
type SyncStateMachineConfig struct {
	// MinSyncPeers is the minimum number of peers required to start syncing.
	MinSyncPeers int

	// InitialSyncThreshold is the number of blocks behind that triggers initial sync.
	// If behind by more than this, use InitialSync; otherwise use CatchUp.
	InitialSyncThreshold uint64

	// CatchUpCheckInterval is how often to check if catch-up is needed.
	CatchUpCheckInterval time.Duration

	// SyncedCheckInterval is how often to verify we're still synced.
	SyncedCheckInterval time.Duration

	// MetricsLogInterval is how often to log metrics.
	MetricsLogInterval time.Duration
}

// DefaultSyncStateMachineConfig returns the default configuration.
func DefaultSyncStateMachineConfig() *SyncStateMachineConfig {
	return &SyncStateMachineConfig{
		MinSyncPeers:         3,
		InitialSyncThreshold: 100,
		CatchUpCheckInterval: 10 * time.Second,
		SyncedCheckInterval:  30 * time.Second,
		MetricsLogInterval:   60 * time.Second,
	}
}

// SyncStateMachine manages the synchronization state transitions.
type SyncStateMachine struct {
	state      int32 // atomic, use SyncState
	blockchain common.IBlockChain
	p2p        p2p.P2P
	config     *SyncStateMachineConfig
	metrics    *SyncMetrics

	ctx    context.Context
	cancel context.CancelFunc

	onStateChange      func(from, to SyncState)
	initialSyncHandler func(ctx context.Context, targetBlock *uint256.Int) error
	catchUpHandler     func(ctx context.Context, targetBlock *uint256.Int) error

	wg sync.WaitGroup
	mu sync.RWMutex
}

// NewSyncStateMachine creates a new sync state machine.
func NewSyncStateMachine(
	ctx context.Context,
	blockchain common.IBlockChain,
	p2p p2p.P2P,
	config *SyncStateMachineConfig,
) *SyncStateMachine {
	if config == nil {
		config = DefaultSyncStateMachineConfig()
	}

	ctx, cancel := context.WithCancel(ctx)

	sm := &SyncStateMachine{
		state:      int32(SyncStateIdle),
		blockchain: blockchain,
		p2p:        p2p,
		config:     config,
		metrics:    NewSyncMetrics(),
		ctx:        ctx,
		cancel:     cancel,
	}

	sm.metrics.EnterState(SyncStateIdle)
	return sm
}

// State returns the current sync state.
func (sm *SyncStateMachine) State() SyncState {
	return SyncState(atomic.LoadInt32(&sm.state))
}

// SetOnStateChange sets the callback for state changes.
func (sm *SyncStateMachine) SetOnStateChange(fn func(from, to SyncState)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onStateChange = fn
}

// SetInitialSyncHandler sets the handler for initial sync.
func (sm *SyncStateMachine) SetInitialSyncHandler(fn func(ctx context.Context, targetBlock *uint256.Int) error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.initialSyncHandler = fn
}

// SetCatchUpHandler sets the handler for catch-up sync.
func (sm *SyncStateMachine) SetCatchUpHandler(fn func(ctx context.Context, targetBlock *uint256.Int) error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.catchUpHandler = fn
}

// transitionTo changes the state machine to a new state.
func (sm *SyncStateMachine) transitionTo(newState SyncState) {
	oldState := SyncState(atomic.SwapInt32(&sm.state, int32(newState)))
	if oldState == newState {
		return
	}

	sm.metrics.ExitState(oldState)
	sm.metrics.EnterState(newState)

	// Log transition (handle nil blockchain for testing)
	log.Info("Sync state transition",
		"from", oldState.String(),
		"to", newState.String(),
		"current_block", currentBlockNumberOrZero(sm.blockchain),
	)

	sm.mu.RLock()
	callback := sm.onStateChange
	sm.mu.RUnlock()

	if callback != nil {
		callback(oldState, newState)
	}
}

// Start begins the state machine loop.
func (sm *SyncStateMachine) Start() {
	sm.goTrack(sm.run)
	sm.goTrack(sm.metricsLogger)
}

// Stop stops the state machine and waits for all tracked goroutines to finish.
func (sm *SyncStateMachine) Stop() {
	sm.cancel()
	sm.wg.Wait()
}

// goTrack launches fn in a goroutine tracked by the WaitGroup so that
// Stop() can wait for it to complete.
func (sm *SyncStateMachine) goTrack(fn func()) {
	sm.wg.Add(1)
	go func() {
		defer sm.wg.Done()
		fn()
	}()
}

// run is the main state machine loop.
func (sm *SyncStateMachine) run() {
	ticker := time.NewTicker(sm.config.CatchUpCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sm.ctx.Done():
			return
		case <-ticker.C:
			sm.evaluate()
		}
	}
}

// evaluate checks the current state and transitions if needed.
func (sm *SyncStateMachine) evaluate() {
	currentState := sm.State()
	currentBlock, err := requireCurrentBlockNumber(sm.blockchain, "current block number unavailable")
	if err != nil {
		log.Warn("Sync state evaluation skipped", "err", err)
		return
	}
	highestBlock, peers := sm.p2p.Peers().BestPeers(sm.config.MinSyncPeers, currentBlock)

	// Update sync metrics.
	nodeMetrics.SyncHighestBlock.Set(highestBlock.Uint64())
	if highestBlock.Uint64() > currentBlock.Uint64() {
		nodeMetrics.SyncIsSyncing.Set(1)
	} else {
		nodeMetrics.SyncIsSyncing.Set(0)
	}

	// Not enough peers
	if len(peers) < sm.config.MinSyncPeers {
		if currentState != SyncStateIdle {
			log.Warn("Not enough peers for sync",
				"have", len(peers),
				"need", sm.config.MinSyncPeers,
			)
		}
		return
	}

	// Calculate how far behind we are
	behindBy := uint64(0)
	if highestBlock.Uint64() > currentBlock.Uint64() {
		behindBy = highestBlock.Uint64() - currentBlock.Uint64()
	}

	switch currentState {
	case SyncStateIdle:
		sm.handleIdleState(behindBy, highestBlock)

	case SyncStateInitialSync:
		sm.handleInitialSyncState(behindBy, highestBlock)

	case SyncStateCatchUp:
		sm.handleCatchUpState(behindBy, highestBlock)

	case SyncStateSynced:
		sm.handleSyncedState(behindBy, highestBlock)
	}
}

// handleIdleState handles transitions from the Idle state.
func (sm *SyncStateMachine) handleIdleState(behindBy uint64, targetBlock *uint256.Int) {
	if behindBy == 0 {
		sm.transitionTo(SyncStateSynced)
		return
	}

	if behindBy > sm.config.InitialSyncThreshold {
		sm.transitionTo(SyncStateInitialSync)
		sm.goTrack(func() { sm.performInitialSync(targetBlock) })
	} else {
		sm.transitionTo(SyncStateCatchUp)
		sm.goTrack(func() { sm.performCatchUp(targetBlock) })
	}
}

// handleInitialSyncState handles transitions from the InitialSync state.
func (sm *SyncStateMachine) handleInitialSyncState(behindBy uint64, targetBlock *uint256.Int) {
	if behindBy == 0 {
		sm.transitionTo(SyncStateSynced)
	} else if behindBy <= sm.config.InitialSyncThreshold {
		sm.transitionTo(SyncStateCatchUp)
		sm.goTrack(func() { sm.performCatchUp(targetBlock) })
	}
}

// handleCatchUpState handles transitions from the CatchUp state.
func (sm *SyncStateMachine) handleCatchUpState(behindBy uint64, targetBlock *uint256.Int) {
	if behindBy == 0 {
		sm.transitionTo(SyncStateSynced)
	} else if behindBy > sm.config.InitialSyncThreshold {
		sm.transitionTo(SyncStateInitialSync)
		sm.goTrack(func() { sm.performInitialSync(targetBlock) })
	}
}

// handleSyncedState handles transitions from the Synced state.
func (sm *SyncStateMachine) handleSyncedState(behindBy uint64, targetBlock *uint256.Int) {
	if behindBy > sm.config.InitialSyncThreshold {
		sm.transitionTo(SyncStateInitialSync)
		sm.goTrack(func() { sm.performInitialSync(targetBlock) })
	} else if behindBy > 0 {
		sm.transitionTo(SyncStateCatchUp)
		sm.goTrack(func() { sm.performCatchUp(targetBlock) })
	}
}

// performInitialSync executes the initial sync process.
func (sm *SyncStateMachine) performInitialSync(targetBlock *uint256.Int) {
	sm.mu.RLock()
	handler := sm.initialSyncHandler
	sm.mu.RUnlock()

	sm.performSync("initial sync", handler, targetBlock, true)
}

// performCatchUp executes the catch-up sync process.
func (sm *SyncStateMachine) performCatchUp(targetBlock *uint256.Int) {
	sm.mu.RLock()
	handler := sm.catchUpHandler
	sm.mu.RUnlock()

	sm.performSync("catch-up sync", handler, targetBlock, false)
}

// performSync is the shared implementation for both initial sync and catch-up.
// The label parameter is used for log messages. When transitionOnFail is true,
// the state machine transitions back to Idle on failure (used for initial sync).
func (sm *SyncStateMachine) performSync(
	label string,
	handler func(ctx context.Context, targetBlock *uint256.Int) error,
	targetBlock *uint256.Int,
	transitionOnFail bool,
) {
	if handler == nil {
		log.Warn("No sync handler configured", "type", label)
		return
	}

	startTime := time.Now()
	startBlock := currentBlockNumberOrZero(sm.blockchain)
	targetBlockNumber := uint64(0)
	if targetBlock != nil {
		targetBlockNumber = targetBlock.Uint64()
	}

	log.Info("Starting sync",
		"type", label,
		"current_block", startBlock,
		"target_block", targetBlockNumber,
	)

	if err := handler(sm.ctx, targetBlock); err != nil {
		log.Error("Sync failed", "type", label, "err", err)
		sm.metrics.RecordBlocksFailed(1)
		if transitionOnFail {
			sm.transitionTo(SyncStateIdle)
		}
		return
	}

	endBlock := currentBlockNumberOrZero(sm.blockchain)
	blocksProcessed := endBlock - startBlock
	sm.metrics.RecordBlocksProcessed(blocksProcessed)

	log.Info("Sync completed",
		"type", label,
		"blocks_synced", blocksProcessed,
		"duration", time.Since(startTime),
		"current_block", endBlock,
	)
}

// metricsLogger periodically logs metrics.
func (sm *SyncStateMachine) metricsLogger() {
	ticker := time.NewTicker(sm.config.MetricsLogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sm.ctx.Done():
			return
		case <-ticker.C:
			sm.metrics.LogStats()
		}
	}
}

// Metrics returns the sync metrics.
func (sm *SyncStateMachine) Metrics() *SyncMetrics {
	return sm.metrics
}

// Syncing returns true if the node is currently syncing.
func (sm *SyncStateMachine) Syncing() bool {
	state := sm.State()
	return state == SyncStateInitialSync || state == SyncStateCatchUp
}

// Synced returns true if the node is fully synced.
func (sm *SyncStateMachine) Synced() bool {
	return sm.State() == SyncStateSynced
}

// Status returns an error if syncing, nil if synced.
func (sm *SyncStateMachine) Status() error {
	if sm.Syncing() {
		return fmt.Errorf("syncing: %s", sm.State().String())
	}
	return nil
}

// Resync triggers a resync operation.
func (sm *SyncStateMachine) Resync() error {
	sm.transitionTo(SyncStateIdle)
	sm.evaluate()
	return nil
}

var _ Checker = (*SyncStateMachine)(nil)
