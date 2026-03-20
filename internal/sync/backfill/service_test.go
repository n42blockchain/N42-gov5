package backfill

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("should be disabled by default")
	}
	if cfg.BatchSize != 64 {
		t.Errorf("BatchSize = %d, want 64", cfg.BatchSize)
	}
	if cfg.Interval != 5*time.Second {
		t.Errorf("Interval = %v, want 5s", cfg.Interval)
	}
}

func TestServiceStartDisabled(t *testing.T) {
	svc := New(nil, nil, nil, DefaultConfig())
	svc.Start(100) // disabled, should return immediately
	// Should be immediately complete since it closes done channel
	select {
	case <-svc.done:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("disabled service should close done channel immediately")
	}
}

func TestServiceStartZeroBlock(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	svc := New(nil, nil, nil, cfg)
	svc.Start(0) // genesis, nothing to backfill
	select {
	case <-svc.done:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("zero-block start should close done channel immediately")
	}
}

func TestServiceProgress(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	svc := New(nil, nil, nil, cfg)
	svc.backfillHead.Store(500)
	svc.totalFilled.Store(100)

	lowest, filled := svc.Progress()
	if lowest != 500 {
		t.Errorf("lowest = %d, want 500", lowest)
	}
	if filled != 100 {
		t.Errorf("filled = %d, want 100", filled)
	}
}

func TestServiceIsComplete(t *testing.T) {
	svc := New(nil, nil, nil, DefaultConfig())
	svc.backfillHead.Store(100)
	if svc.IsComplete() {
		t.Error("should not be complete at block 100")
	}
	svc.backfillHead.Store(0)
	if !svc.IsComplete() {
		t.Error("should be complete at block 0")
	}
}

func TestServiceStopIdempotent(t *testing.T) {
	cfg := DefaultConfig()
	svc := New(nil, nil, nil, cfg)
	svc.Start(0) // disabled path
	svc.Stop()   // should not panic
}
