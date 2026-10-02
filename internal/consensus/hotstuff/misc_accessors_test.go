package hotstuff

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// TestAPI_NilEngine covers the API accessors' fallback branches when the
// underlying HotStuff has no initialized ConsensusEngine yet.
func TestAPI_NilEngine(t *testing.T) {
	h := &HotStuff{}
	api := &API{hotstuff: h}

	if v := api.GetCurrentView(); v != 0 {
		t.Fatalf("GetCurrentView: expected 0, got %v", v)
	}
	if p := api.GetCurrentPhase(); p != "uninitialized" {
		t.Fatalf("GetCurrentPhase: expected uninitialized, got %q", p)
	}
	if c := api.GetValidatorCount(); c != 0 {
		t.Fatalf("GetValidatorCount: expected 0, got %v", c)
	}
	if c := api.GetConsecutiveTimeouts(); c != 0 {
		t.Fatalf("GetConsecutiveTimeouts: expected 0, got %v", c)
	}
	if l := api.IsCurrentLeader(); l {
		t.Fatalf("IsCurrentLeader: expected false, got true")
	}
}

// TestSortedAddrs confirms a deterministic, byte-order sorted address list.
func TestSortedAddrs(t *testing.T) {
	var a, b, c types.Address
	a[0] = 0x03
	b[0] = 0x01
	c[0] = 0x02

	got := sortedAddrs(map[types.Address]*account.StateAccount{
		a: nil,
		b: nil,
		c: nil,
	})
	if len(got) != 3 {
		t.Fatalf("expected 3 addrs, got %d", len(got))
	}
	if got[0] != b || got[1] != c || got[2] != a {
		t.Fatalf("addresses not sorted: %v", got)
	}
}

// TestErrors_Error covers the Error() string formatting for the typed
// consensus errors.
func TestErrors_Error(t *testing.T) {
	var h1, h2 types.Hash
	h1[0] = 0xaa
	h2[0] = 0xbb

	bhm := &BlockHashMismatchError{Expected: h1, Got: h2}
	if bhm.Error() == "" {
		t.Fatalf("BlockHashMismatchError.Error() empty")
	}

	dav := &DAVerificationError{BlockHash: h1, ExpectedRoot: h1, ActualRoot: h2}
	if dav.Error() == "" {
		t.Fatalf("DAVerificationError.Error() empty")
	}
}

// TestPacemaker_SimpleAccessors covers Deadline/Stop/TimeoutChan/StopTimer
// and WatchContext's single-shot guard.
func TestPacemaker_SimpleAccessors(t *testing.T) {
	pm := NewPacemaker(10, 100)

	dl := pm.Deadline()
	if dl.IsZero() {
		t.Fatalf("expected non-zero deadline")
	}

	ch := pm.TimeoutChan()
	if ch == nil {
		t.Fatalf("expected non-nil timeout channel before stop")
	}
	// Second call reuses the timer (Stop/Reset branch).
	ch2 := pm.TimeoutChan()
	if ch2 == nil {
		t.Fatalf("expected non-nil timeout channel on reuse")
	}

	pm.StopTimer()

	pm.Stop()
	if ch3 := pm.TimeoutChan(); ch3 != nil {
		t.Fatalf("expected nil channel after Stop()")
	}

	// WatchContext: first call starts the watcher goroutine, second call
	// is a no-op because watchStarted is already true.
	ctx, cancel := context.WithCancel(context.Background())
	pm2 := NewPacemaker(10, 100)
	pm2.WatchContext(ctx)
	pm2.WatchContext(ctx) // no-op branch (watchStarted)
	cancel()
	// Give the watcher goroutine a bounded chance to run; not required for
	// correctness, only to exercise the code path deterministically-ish.
	time.Sleep(20 * time.Millisecond)
	pm2.StopTimer()

	// WatchContext after Stop() is also a no-op.
	pm3 := NewPacemaker(10, 100)
	pm3.Stop()
	pm3.WatchContext(context.Background())
}

// TestMetrics_Updaters exercises the small counter-update helper functions;
// they have no observable return value, so this just confirms they don't
// panic and can be called repeatedly.
func TestMetrics_Updaters(t *testing.T) {
	updateMetricsBlockCommitted(ViewNumber(5))
	updateMetricsViewChanged(ViewNumber(6))
	updateMetricsTimeout()
	updateMetricsEquivocation()
}
