// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import "testing"

// TestSelectMDBXFlags pins the post-incident default policy:
// WriteMap is OFF by default. Enabling it on the Windows hosts we run
// forward-replay on causes every written page to join the process
// working set; the kernel keeps those pages resident, and the WS grows
// to MDBX-file-size (~80 GB at 16M blocks) → pagefile thrash on 128 GB
// hosts. Reproduced three times before the fix landed (15:02:45 was
// the last OOM with cache-storage-gb=16 + memory-limit-gb=50, proving
// it's not a Go-heap problem). If a future change flips the default
// back to WriteMap=true silently, this test fires.
func TestSelectMDBXFlags(t *testing.T) {
	cases := []struct {
		name         string
		writeMap     bool
		safeDurable  bool
		wantWriteMap bool
		wantNoSync   bool
		wantLabel    string
	}{
		{
			name:         "default (writeMap=false, safeDurable=false)",
			writeMap:     false,
			safeDurable:  false,
			wantWriteMap: false,
			wantNoSync:   true,
			wantLabel:    "SafeNoSync+LifoReclaim",
		},
		{
			name:         "writemap opt-in (Linux fast path)",
			writeMap:     true,
			safeDurable:  false,
			wantWriteMap: true,
			wantNoSync:   true,
			wantLabel:    "WriteMap+SafeNoSync+LifoReclaim",
		},
		{
			name:         "safe-durable (full fsync)",
			writeMap:     false,
			safeDurable:  true,
			wantWriteMap: false,
			wantNoSync:   false,
			wantLabel:    "LifoReclaim",
		},
		{
			name:         "writemap + safe-durable (uncommon, but legal)",
			writeMap:     true,
			safeDurable:  true,
			wantWriteMap: true,
			wantNoSync:   false,
			wantLabel:    "WriteMap+LifoReclaim",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotWriteMap, gotNoSync, gotLabel := selectMDBXFlags(tc.writeMap, tc.safeDurable)
			if gotWriteMap != tc.wantWriteMap {
				t.Errorf("WriteMap: got %v want %v", gotWriteMap, tc.wantWriteMap)
			}
			if gotNoSync != tc.wantNoSync {
				t.Errorf("SafeNoSync: got %v want %v", gotNoSync, tc.wantNoSync)
			}
			if gotLabel != tc.wantLabel {
				t.Errorf("label: got %q want %q", gotLabel, tc.wantLabel)
			}
		})
	}
}

// TestSelectMDBXFlags_DefaultIsNoWriteMap is the explicit regression
// guard: the no-arg call (zero values, mimicking absent flags) must
// produce useWriteMap=false. The named test above also covers this,
// but a focused single-assertion test makes the intent obvious.
func TestSelectMDBXFlags_DefaultIsNoWriteMap(t *testing.T) {
	useWriteMap, _, _ := selectMDBXFlags(false, false)
	if useWriteMap {
		t.Fatal("default WriteMap MUST be false on Windows hosts — see commit 1cc21b50 for the OOM reproduction")
	}
}
