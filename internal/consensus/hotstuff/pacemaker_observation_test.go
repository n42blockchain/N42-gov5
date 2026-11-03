package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestCommittedViewsAdaptNextDeadline(t *testing.T) {
	setup := newTestSetup(t, 1)
	engine, outputs := newTestEngine(t, setup, 0)
	engine.pacemaker = NewPacemaker(6000, 30000)
	for i := 0; i < 5; i++ {
		// Model a completed heavy view without sleeping. Exercise the real
		// commit -> advanceToView -> ResetForView path, not only the estimator.
		engine.mu.Lock()
		engine.viewTiming.ViewStart = time.Now().Add(-8 * time.Second)
		engine.mu.Unlock()
		if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: types.Hash{byte(i + 1)}}); err != nil {
			t.Fatal(err)
		}
		drainOutputs(outputs)
	}
	if got := engine.pacemaker.TimeoutDuration(0); got < 16*time.Second || got > 30*time.Second {
		t.Fatalf("adaptive base = %v, want 16..30s after completed heavy views", got)
	}
	if remaining := engine.pacemaker.Remaining(); remaining < 15*time.Second {
		t.Fatalf("next view retained stale base deadline: %v", remaining)
	}
}

func TestUncommittedViewDoesNotTrainPacemaker(t *testing.T) {
	setup := newTestSetup(t, 1)
	engine, outputs := newTestEngine(t, setup, 0)
	engine.pacemaker = NewPacemaker(6000, 30000)
	for i := 0; i < 6; i++ {
		engine.mu.Lock()
		engine.viewTiming.ViewStart = time.Now().Add(-20 * time.Second)
		err := engine.advanceToView(engine.roundState.CurrentView() + 1)
		engine.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		drainOutputs(outputs)
	}
	if got := engine.pacemaker.TimeoutDuration(0); got != 6*time.Second {
		t.Fatalf("uncommitted views changed base timeout: %v", got)
	}
}
