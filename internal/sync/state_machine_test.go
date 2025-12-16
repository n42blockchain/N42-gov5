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
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
)

// =============================================================================
// SyncState Tests
// =============================================================================

func TestSyncStateString(t *testing.T) {
	tests := []struct {
		state    SyncState
		expected string
	}{
		{SyncStateIdle, "Idle"},
		{SyncStateInitialSync, "InitialSync"},
		{SyncStateCatchUp, "CatchUp"},
		{SyncStateSynced, "Synced"},
		{SyncState(99), "Unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("SyncState.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestSyncStateValues(t *testing.T) {
	// Verify state values are as expected
	if SyncStateIdle != 0 {
		t.Errorf("SyncStateIdle = %d, want 0", SyncStateIdle)
	}
	if SyncStateInitialSync != 1 {
		t.Errorf("SyncStateInitialSync = %d, want 1", SyncStateInitialSync)
	}
	if SyncStateCatchUp != 2 {
		t.Errorf("SyncStateCatchUp = %d, want 2", SyncStateCatchUp)
	}
	if SyncStateSynced != 3 {
		t.Errorf("SyncStateSynced = %d, want 3", SyncStateSynced)
	}
}

// =============================================================================
// SyncMetrics Tests
// =============================================================================

func TestNewSyncMetrics(t *testing.T) {
	m := NewSyncMetrics()
	if m == nil {
		t.Fatal("NewSyncMetrics() returned nil")
	}
	if m.stateEnterTime == nil {
		t.Error("stateEnterTime map not initialized")
	}
	if m.stateDuration == nil {
		t.Error("stateDuration map not initialized")
	}
}

func TestSyncMetricsStateDuration(t *testing.T) {
	m := NewSyncMetrics()

	m.EnterState(SyncStateInitialSync)
	time.Sleep(10 * time.Millisecond)
	m.ExitState(SyncStateInitialSync)

	duration := m.StateDuration(SyncStateInitialSync)
	if duration < 10*time.Millisecond {
		t.Errorf("StateDuration = %v, want >= 10ms", duration)
	}
}

func TestSyncMetricsBlocksProcessed(t *testing.T) {
	m := NewSyncMetrics()

	m.RecordBlocksProcessed(100)
	m.RecordBlocksProcessed(50)

	m.mu.RLock()
	total := m.blocksProcessed
	m.mu.RUnlock()

	if total != 150 {
		t.Errorf("blocksProcessed = %d, want 150", total)
	}
}

func TestSyncMetricsFailureRate(t *testing.T) {
	m := NewSyncMetrics()

	// No blocks yet
	if rate := m.FailureRate(); rate != 0 {
		t.Errorf("FailureRate() = %v, want 0", rate)
	}

	m.RecordBlocksProcessed(80)
	m.RecordBlocksFailed(20)

	rate := m.FailureRate()
	expected := 0.2 // 20 / 100
	if rate != expected {
		t.Errorf("FailureRate() = %v, want %v", rate, expected)
	}
}

func TestSyncMetricsConcurrency(t *testing.T) {
	m := NewSyncMetrics()
	var wg sync.WaitGroup

	// Concurrent operations
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.RecordBlocksProcessed(1)
			m.RecordBlocksFailed(0)
			m.RecordDisconnect()
			m.EnterState(SyncStateIdle)
			m.ExitState(SyncStateIdle)
			_ = m.FailureRate()
			_ = m.BlocksPerSecond()
		}()
	}

	wg.Wait()
	t.Log("✓ SyncMetrics concurrent operations completed without race")
}

// =============================================================================
// SyncStateMachineConfig Tests
// =============================================================================

func TestDefaultSyncStateMachineConfig(t *testing.T) {
	config := DefaultSyncStateMachineConfig()

	if config.MinSyncPeers != 3 {
		t.Errorf("MinSyncPeers = %d, want 3", config.MinSyncPeers)
	}
	if config.InitialSyncThreshold != 100 {
		t.Errorf("InitialSyncThreshold = %d, want 100", config.InitialSyncThreshold)
	}
	if config.CatchUpCheckInterval != 10*time.Second {
		t.Errorf("CatchUpCheckInterval = %v, want 10s", config.CatchUpCheckInterval)
	}
	if config.SyncedCheckInterval != 30*time.Second {
		t.Errorf("SyncedCheckInterval = %v, want 30s", config.SyncedCheckInterval)
	}
	if config.MetricsLogInterval != 60*time.Second {
		t.Errorf("MetricsLogInterval = %v, want 60s", config.MetricsLogInterval)
	}
}

// =============================================================================
// SyncStateMachine Tests (Unit tests without blockchain/p2p)
// =============================================================================

func TestSyncStateMachineInitialState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := NewSyncStateMachine(ctx, nil, nil, nil)
	if sm == nil {
		t.Fatal("NewSyncStateMachine() returned nil")
	}

	if sm.State() != SyncStateIdle {
		t.Errorf("Initial state = %v, want Idle", sm.State())
	}
}

func TestSyncStateMachineTransitionTo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := NewSyncStateMachine(ctx, nil, nil, nil)

	var transitions []struct {
		from, to SyncState
	}
	var mu sync.Mutex

	sm.SetOnStateChange(func(from, to SyncState) {
		mu.Lock()
		transitions = append(transitions, struct{ from, to SyncState }{from, to})
		mu.Unlock()
	})

	// Test transitions
	sm.transitionTo(SyncStateInitialSync)
	sm.transitionTo(SyncStateCatchUp)
	sm.transitionTo(SyncStateSynced)
	sm.transitionTo(SyncStateSynced) // Same state, should not trigger callback

	mu.Lock()
	defer mu.Unlock()

	if len(transitions) != 3 {
		t.Errorf("transitions = %d, want 3", len(transitions))
	}

	if transitions[0].from != SyncStateIdle || transitions[0].to != SyncStateInitialSync {
		t.Errorf("First transition wrong: %v -> %v", transitions[0].from, transitions[0].to)
	}
}

func TestSyncStateMachineSyncing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := NewSyncStateMachine(ctx, nil, nil, nil)

	// Idle - not syncing
	if sm.Syncing() {
		t.Error("Syncing() should be false in Idle state")
	}

	// InitialSync - syncing
	sm.transitionTo(SyncStateInitialSync)
	if !sm.Syncing() {
		t.Error("Syncing() should be true in InitialSync state")
	}

	// CatchUp - syncing
	sm.transitionTo(SyncStateCatchUp)
	if !sm.Syncing() {
		t.Error("Syncing() should be true in CatchUp state")
	}

	// Synced - not syncing
	sm.transitionTo(SyncStateSynced)
	if sm.Syncing() {
		t.Error("Syncing() should be false in Synced state")
	}
}

func TestSyncStateMachineSynced(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := NewSyncStateMachine(ctx, nil, nil, nil)

	// Idle - not synced
	if sm.Synced() {
		t.Error("Synced() should be false in Idle state")
	}

	// Synced - synced
	sm.transitionTo(SyncStateSynced)
	if !sm.Synced() {
		t.Error("Synced() should be true in Synced state")
	}
}

func TestSyncStateMachineStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := NewSyncStateMachine(ctx, nil, nil, nil)

	// Synced - no error
	sm.transitionTo(SyncStateSynced)
	if err := sm.Status(); err != nil {
		t.Errorf("Status() = %v, want nil in Synced state", err)
	}

	// Syncing - error
	sm.transitionTo(SyncStateInitialSync)
	if err := sm.Status(); err == nil {
		t.Error("Status() should return error in InitialSync state")
	}
}

func TestSyncStateMachineStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := NewSyncStateMachine(ctx, nil, nil, nil)
	sm.Stop()

	select {
	case <-sm.ctx.Done():
		// Expected
	default:
		t.Error("Stop() should cancel context")
	}
}

func TestSyncStateMachineHandlers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := NewSyncStateMachine(ctx, nil, nil, nil)

	initialSyncCalled := false
	catchUpCalled := false

	sm.SetInitialSyncHandler(func(ctx context.Context, targetBlock *uint256.Int) error {
		initialSyncCalled = true
		return nil
	})

	sm.SetCatchUpHandler(func(ctx context.Context, targetBlock *uint256.Int) error {
		catchUpCalled = true
		return nil
	})

	// Test initial sync handler
	sm.performInitialSync(uint256.NewInt(1000))
	if !initialSyncCalled {
		t.Error("Initial sync handler not called")
	}

	// Test catch-up handler
	sm.performCatchUp(uint256.NewInt(1000))
	if !catchUpCalled {
		t.Error("Catch-up handler not called")
	}
}

func TestSyncStateMachineMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := NewSyncStateMachine(ctx, nil, nil, nil)

	metrics := sm.Metrics()
	if metrics == nil {
		t.Error("Metrics() returned nil")
	}

	// Should be the same instance
	if sm.metrics != metrics {
		t.Error("Metrics() returned different instance")
	}
}

// =============================================================================
// Golden Sample Tests
// =============================================================================

func TestGoldenSampleStateTransitions(t *testing.T) {
	// Test deterministic state transitions
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := NewSyncStateMachine(ctx, nil, nil, nil)

	// Golden path: Idle -> InitialSync -> CatchUp -> Synced
	expectedPath := []SyncState{
		SyncStateIdle,
		SyncStateInitialSync,
		SyncStateCatchUp,
		SyncStateSynced,
	}

	for i, expected := range expectedPath {
		if i > 0 {
			sm.transitionTo(expected)
		}
		if sm.State() != expected {
			t.Errorf("Step %d: State() = %v, want %v", i, sm.State(), expected)
		}
	}
}

// =============================================================================
// Checker Interface Compliance
// =============================================================================

func TestSyncStateMachineImplementsChecker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var checker Checker = NewSyncStateMachine(ctx, nil, nil, nil)
	_ = checker

	t.Log("✓ SyncStateMachine implements Checker interface")
}

