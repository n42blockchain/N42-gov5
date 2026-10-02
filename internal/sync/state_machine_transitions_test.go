package sync

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
)

// newTestStateMachine builds a SyncStateMachine with no handlers configured
// (performSync's handler==nil path logs and returns immediately), so the
// goroutines handleIdleState/etc. launch via goTrack complete synchronously
// once Stop() waits on them.
func newTestStateMachine(t *testing.T) (*SyncStateMachine, *fakeP2P) {
	t.Helper()
	fp := newFakeP2P(t)
	chain := &syncChainStub{current: &syncBlockStub{number: uint256.NewInt(0)}}
	sm := NewSyncStateMachine(context.Background(), chain, fp, nil)
	return sm, fp
}

func TestHandleIdleStateTransitionsToSyncedWhenCaughtUp(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.handleIdleState(0, uint256.NewInt(100))
	if sm.State() != SyncStateSynced {
		t.Fatalf("state = %v, want Synced", sm.State())
	}
}

func TestHandleIdleStateTransitionsToInitialSyncWhenFarBehind(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.handleIdleState(sm.config.InitialSyncThreshold+1, uint256.NewInt(1000))
	if sm.State() != SyncStateInitialSync {
		t.Fatalf("state = %v, want InitialSync", sm.State())
	}
	sm.Stop()
}

func TestHandleIdleStateTransitionsToCatchUpWhenCloseBehind(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.handleIdleState(1, uint256.NewInt(1))
	if sm.State() != SyncStateCatchUp {
		t.Fatalf("state = %v, want CatchUp", sm.State())
	}
	sm.Stop()
}

func TestHandleInitialSyncStateTransitionsToSynced(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateInitialSync)
	sm.handleInitialSyncState(0, uint256.NewInt(0))
	if sm.State() != SyncStateSynced {
		t.Fatalf("state = %v, want Synced", sm.State())
	}
}

func TestHandleInitialSyncStateTransitionsToCatchUp(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateInitialSync)
	sm.handleInitialSyncState(1, uint256.NewInt(1))
	if sm.State() != SyncStateCatchUp {
		t.Fatalf("state = %v, want CatchUp", sm.State())
	}
	sm.Stop()
}

func TestHandleInitialSyncStateStaysWhenStillFarBehind(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateInitialSync)
	sm.handleInitialSyncState(sm.config.InitialSyncThreshold+1, uint256.NewInt(1000))
	if sm.State() != SyncStateInitialSync {
		t.Fatalf("state = %v, want to remain InitialSync", sm.State())
	}
}

func TestHandleCatchUpStateTransitionsToSynced(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateCatchUp)
	sm.handleCatchUpState(0, uint256.NewInt(0))
	if sm.State() != SyncStateSynced {
		t.Fatalf("state = %v, want Synced", sm.State())
	}
}

func TestHandleCatchUpStateTransitionsToInitialSyncWhenFallingBehind(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateCatchUp)
	sm.handleCatchUpState(sm.config.InitialSyncThreshold+1, uint256.NewInt(1000))
	if sm.State() != SyncStateInitialSync {
		t.Fatalf("state = %v, want InitialSync", sm.State())
	}
	sm.Stop()
}

func TestHandleSyncedStateTransitionsToInitialSync(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateSynced)
	sm.handleSyncedState(sm.config.InitialSyncThreshold+1, uint256.NewInt(1000))
	if sm.State() != SyncStateInitialSync {
		t.Fatalf("state = %v, want InitialSync", sm.State())
	}
	sm.Stop()
}

func TestHandleSyncedStateTransitionsToCatchUp(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateSynced)
	sm.handleSyncedState(1, uint256.NewInt(1))
	if sm.State() != SyncStateCatchUp {
		t.Fatalf("state = %v, want CatchUp", sm.State())
	}
	sm.Stop()
}

func TestHandleSyncedStateStaysWhenCaughtUp(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateSynced)
	sm.handleSyncedState(0, uint256.NewInt(0))
	if sm.State() != SyncStateSynced {
		t.Fatalf("state = %v, want to remain Synced", sm.State())
	}
}

func TestStatusReportsErrorWhileSyncing(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateInitialSync)
	if err := sm.Status(); err == nil {
		t.Fatal("expected Status() to report an error while syncing")
	}
}

func TestStatusNilWhenSynced(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateSynced)
	if err := sm.Status(); err != nil {
		t.Fatalf("Status() = %v, want nil when synced", err)
	}
}

func TestResyncTransitionsToIdleAndEvaluates(t *testing.T) {
	sm, _ := newTestStateMachine(t)
	sm.transitionTo(SyncStateSynced)
	// evaluate() requires a current block number and peers; with no peers
	// configured on fakeP2P, evaluate() returns early after the not-enough-
	// peers check, leaving the state at Idle (set by Resync itself).
	if err := sm.Resync(); err != nil {
		t.Fatalf("Resync() = %v, want nil", err)
	}
	if sm.State() != SyncStateIdle {
		t.Fatalf("state = %v, want Idle after Resync with no peers to re-sync against", sm.State())
	}
}
