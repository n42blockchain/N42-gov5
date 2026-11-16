// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"runtime"
	"testing"
)

// Every GC stamp metric exists in this runtime, and a forced cycle between
// two calls shows up as a completed cycle.
func TestGCBlockFields(t *testing.T) {
	gcBlockFields()
	for i, s := range gcStamp.samples {
		if s.Value.Kind() == 0 {
			t.Fatalf("metric %s unsupported", gcStampNames[i])
		}
	}
	runtime.GC()
	f := gcBlockFields()
	if len(f) != 14 || f[0] != "gcCycles" || f[1].(int64) < 1 || f[11].(int64) < 0 {
		t.Fatalf("fields %v", f)
	}
}
