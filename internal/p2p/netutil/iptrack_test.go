package netutil

import (
	"testing"
	"time"
)

func TestIPTrackerPredictEndpoint(t *testing.T) {
	it := NewIPTracker(time.Minute, time.Minute, 2)

	// No statements yet.
	if got := it.PredictEndpoint(); got != "" {
		t.Fatalf("expected empty prediction, got %q", got)
	}

	it.AddStatement("host1", "1.2.3.4:30303")
	// minStatements is 2, a single statement should not yet be enough.
	if got := it.PredictEndpoint(); got != "" {
		t.Fatalf("expected empty prediction with 1 statement, got %q", got)
	}

	it.AddStatement("host2", "1.2.3.4:30303")
	if got := it.PredictEndpoint(); got != "1.2.3.4:30303" {
		t.Fatalf("PredictEndpoint() = %q, want 1.2.3.4:30303", got)
	}

	// A minority statement shouldn't change the prediction.
	it.AddStatement("host3", "5.6.7.8:30303")
	if got := it.PredictEndpoint(); got != "1.2.3.4:30303" {
		t.Fatalf("PredictEndpoint() = %q, want majority endpoint unchanged", got)
	}
}

func TestIPTrackerPredictFullConeNAT(t *testing.T) {
	it := NewIPTracker(time.Minute, time.Minute, 1)

	// No statements at all: not behind full cone NAT.
	if it.PredictFullConeNAT() {
		t.Fatal("expected false with no statements")
	}

	// We contacted host1 before it made a statement: not full cone.
	it.AddContact("host1")
	it.AddStatement("host1", "1.2.3.4:30303")
	if it.PredictFullConeNAT() {
		t.Fatal("expected false when contact precedes statement")
	}

	// host2 made a statement without any prior contact: full cone NAT.
	it.AddStatement("host2", "9.9.9.9:30303")
	if !it.PredictFullConeNAT() {
		t.Fatal("expected true when a statement arrives from an uncontacted host")
	}
}

func TestIPTrackerGC(t *testing.T) {
	// Use a very small window so GC logic executes within the test without
	// needing a long sleep.
	it := NewIPTracker(10*time.Millisecond, 10*time.Millisecond, 1)
	it.AddStatement("host1", "1.2.3.4:30303")
	it.AddContact("host1")

	time.Sleep(30 * time.Millisecond)

	// Adding a new statement triggers gcStatements/gcContact for the old entries.
	it.AddStatement("host2", "5.6.7.8:30303")
	it.AddContact("host2")

	// The prediction should now reflect only the fresh, non-expired statement.
	if got := it.PredictEndpoint(); got != "5.6.7.8:30303" {
		t.Fatalf("PredictEndpoint() = %q, want 5.6.7.8:30303 after GC", got)
	}
}
