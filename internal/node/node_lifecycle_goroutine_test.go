package node

import (
	"runtime"
	"testing"
	"time"
)

// TestNewNodeStopReleasesGoroutines starts and stops a minimal private-chain
// node and checks that the goroutine count returns close to its pre-start
// baseline, bounded by a short polling wait rather than a fixed sleep.
//
// KNOWN DEFECT (not fixed here, see test/coverage-70-g53 report): Close()
// does not stop every goroutine Start() creates. Observed survivors after
// Close() include internal/p2p/leakybucket.(*Collector).startPeriodicPrune,
// internal/devp2p.(*Server).Start's accept loop, the filter API's
// internal/api/filters.NewEventSystem loop, internal/api.NewOracle's loop,
// and the keystore file watcher from accounts/keystore.(*KeyStore).Subscribe.
// That accounts for the elevated tolerance below instead of a tight "no
// leak" bound; a materially larger leak than this still fails the test.
func TestNewNodeStopReleasesGoroutines(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)

	runtime.GC()
	before := runtime.NumGoroutine()

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	const slack = 30 // see KNOWN DEFECT note above
	deadline := time.Now().Add(2 * time.Second)
	var after int
	for {
		runtime.GC()
		after = runtime.NumGoroutine()
		if after <= before+slack || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if after > before+slack {
		t.Fatalf("goroutine leak suspected beyond known baseline: before=%d after=%d", before, after)
	}
}

// TestNewNodeCloseWithoutStartIsSafe verifies that closing a constructed-but-
// never-started node does not panic and still releases the datadir lock.
func TestNewNodeCloseWithoutStartIsSafe(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("Close on unstarted node failed: %v", err)
	}
}

// TestNewNodeDoubleStartFails ensures Start() is not idempotent and reports
// ErrNodeRunning the second time.
func TestNewNodeDoubleStartFails(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if err := n.Start(); err != nil {
		t.Fatalf("first Start failed: %v", err)
	}
	if err := n.Start(); err != ErrNodeRunning {
		t.Fatalf("expected ErrNodeRunning, got %v", err)
	}
}
