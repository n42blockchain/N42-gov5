package dbg

import (
	"testing"
	"time"
)

func TestNewLeakDetectorZeroThreshold(t *testing.T) {
	if d := NewLeakDetector("test", 0); d != nil {
		t.Fatal("expected nil detector for non-positive threshold")
	}
	if d := NewLeakDetector("test", -time.Second); d != nil {
		t.Fatal("expected nil detector for negative threshold")
	}
}

func TestLeakDetectorNilReceiverIsNoop(t *testing.T) {
	// NOTE: Enabled() itself is not nil-safe (it dereferences d.enabled
	// directly), so it is only ever called on a nil receiver through the
	// `d == nil || !d.Enabled()` short-circuit guards in Add/Del/slowList.
	// Calling d.Enabled() directly here would panic; exercise the guarded
	// entry points instead.
	var d *LeakDetector
	if id := d.Add(); id != 0 {
		t.Fatalf("nil detector Add() = %d, want 0", id)
	}
	d.Del(1) // must not panic
	if list := d.slowList(); list != nil {
		t.Fatalf("nil detector slowList() = %v, want nil", list)
	}
}

func TestLeakDetectorAddDel(t *testing.T) {
	d := NewLeakDetector("test-add-del", time.Hour)
	if d == nil {
		t.Fatal("expected non-nil detector")
	}
	if !d.Enabled() {
		t.Fatal("expected detector to be enabled")
	}

	id := d.Add()
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	// Not aged past threshold yet, so slowList should be empty.
	if list := d.slowList(); len(list) != 0 {
		t.Fatalf("slowList = %v, want empty (threshold not exceeded)", list)
	}

	d.Del(id)

	// Deleting again should be a harmless no-op.
	d.Del(id)
}

func TestLeakDetectorSlowThresholdAndDisable(t *testing.T) {
	d := NewLeakDetector("test-threshold", time.Hour)
	if d == nil {
		t.Fatal("expected non-nil detector")
	}

	// Lower threshold so the previously-added item (if any) would show as slow.
	d.SetSlowThreshold(time.Nanosecond)
	id := d.Add()
	time.Sleep(time.Millisecond)

	list := d.slowList()
	if len(list) == 0 {
		t.Fatal("expected at least one slow entry")
	}
	d.Del(id)

	// Disabling via a zero/negative threshold.
	d.SetSlowThreshold(0)
	if d.Enabled() {
		t.Fatal("expected detector to be disabled after SetSlowThreshold(0)")
	}
	if list := d.slowList(); list != nil {
		t.Fatalf("slowList on disabled detector = %v, want nil", list)
	}
	// Add/Del should now be no-ops.
	if id := d.Add(); id != 0 {
		t.Fatalf("Add() on disabled detector = %d, want 0", id)
	}
}

func TestLeakDetectorMaxScanEntries(t *testing.T) {
	d := NewLeakDetector("test-scan", time.Nanosecond)
	if d == nil {
		t.Fatal("expected non-nil detector")
	}
	// Add more than maxScanEntries items so slowList's early-exit path runs.
	for i := 0; i < maxScanEntries+5; i++ {
		d.Add()
	}
	time.Sleep(time.Millisecond)
	list := d.slowList()
	if len(list) == 0 {
		t.Fatal("expected slow entries to be reported")
	}
	if len(list) > maxScanEntries {
		t.Fatalf("slowList returned %d entries, want at most %d", len(list), maxScanEntries)
	}
}
