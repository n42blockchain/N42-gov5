// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import "testing"

// stubColdResolver is a minimal ColdResolver for exercising the
// Set/GetDefaultColdResolver install point without any real cold storage.
type stubColdResolver struct{}

func (stubColdResolver) Resolve(blockNum uint64) (string, error) {
	return "", nil
}

// TestDefaultColdResolver_SetAndGet confirms the process-wide default is
// nil until explicitly set, round-trips a non-nil value, and can be reset
// to nil.
func TestDefaultColdResolver_SetAndGet(t *testing.T) {
	// Save/restore so this test doesn't leak global state into others.
	orig := DefaultColdResolver()
	t.Cleanup(func() { SetDefaultColdResolver(orig) })

	SetDefaultColdResolver(nil)
	if DefaultColdResolver() != nil {
		t.Fatalf("expected nil default resolver")
	}

	cr := stubColdResolver{}
	SetDefaultColdResolver(cr)
	if DefaultColdResolver() != cr {
		t.Fatalf("expected the installed resolver back")
	}

	SetDefaultColdResolver(nil)
	if DefaultColdResolver() != nil {
		t.Fatalf("expected nil after reset")
	}
}
