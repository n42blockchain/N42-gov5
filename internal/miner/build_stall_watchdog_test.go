// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// countDumps returns the names of every *.stacks file in dir.
func countDumps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".stacks") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestBuildStallWatchdogDisabledIsNil(t *testing.T) {
	if wd := newBuildStallWatchdog(false, t.TempDir()); wd != nil {
		t.Fatalf("newBuildStallWatchdog(false, ...) = %v, want nil", wd)
	}
}

// TestBuildStallWatchdogNilMethodsAreNoOps documents and locks in that every
// method on a nil watchdog is safe: commitWork calls these unconditionally
// so it never needs its own diag-enabled branch.
func TestBuildStallWatchdogNilMethodsAreNoOps(t *testing.T) {
	var wd *buildStallWatchdog
	wd.SetStep("whatever")
	wd.SetBlockNumber(42)
	wd.Cancel() // must not panic
}

func TestBuildStallWatchdogFiresAfterThreshold(t *testing.T) {
	lastBuildStallDumpUnixNano.Store(0) // this test expects the dump to be allowed
	dir := t.TempDir()

	wd := newBuildStallWatchdogWithThreshold(true, dir, 20*time.Millisecond)
	if wd == nil {
		t.Fatal("expected an armed watchdog")
	}
	wd.SetBlockNumber(13659711)
	wd.SetStep("alignAppliedBranch")

	deadline := time.After(2 * time.Second)
	for {
		if dumps := countDumps(t, dir); len(dumps) == 1 {
			data, err := os.ReadFile(filepath.Join(dir, dumps[0]))
			if err != nil {
				t.Fatalf("read dump: %v", err)
			}
			if !strings.Contains(string(data), "goroutine ") {
				t.Fatalf("dump does not look like a goroutine stack: %q", string(data))
			}
			if !strings.Contains(dumps[0], "build-stall-13659711-") {
				t.Fatalf("dump name %q does not carry the block number", dumps[0])
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("watchdog did not write a dump within the deadline")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestBuildStallWatchdogCancelPreventsDump(t *testing.T) {
	lastBuildStallDumpUnixNano.Store(0)
	dir := t.TempDir()

	wd := newBuildStallWatchdogWithThreshold(true, dir, 20*time.Millisecond)
	wd.Cancel() // the fill "started" before the threshold

	time.Sleep(150 * time.Millisecond) // long past the threshold
	if dumps := countDumps(t, dir); len(dumps) != 0 {
		t.Fatalf("cancelled watchdog still dumped: %v", dumps)
	}
}

func TestBuildStallWatchdogRateLimited(t *testing.T) {
	lastBuildStallDumpUnixNano.Store(0)
	dir := t.TempDir()

	wd1 := newBuildStallWatchdogWithThreshold(true, dir, 10*time.Millisecond)
	waitForDumpCount(t, dir, 1)
	_ = wd1

	// A second stall within the 60 s rate-limit window must not add a
	// second dump: one occurrence per process per interval already answers
	// the question, and a wedged fleet retrying the stall must not turn
	// into a dump-per-attempt storm.
	wd2 := newBuildStallWatchdogWithThreshold(true, dir, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // long enough for wd2 to have fired too
	wd2.Cancel()

	if dumps := countDumps(t, dir); len(dumps) != 1 {
		t.Fatalf("expected exactly one dump under the rate limit, got %v", dumps)
	}
}

func waitForDumpCount(t *testing.T, dir string, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if len(countDumps(t, dir)) >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d dump(s) in %s", want, dir)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestWriteGoroutineDumpFallsBackToStderrWhenDirEmpty(t *testing.T) {
	// dir == "" must not attempt a file write; the caller (fire) treats ""
	// as "unreachable log directory" and the function must return "" so the
	// log line correctly reports no path.
	if path := writeGoroutineDump("", 1); path != "" {
		t.Fatalf("writeGoroutineDump(\"\", ...) = %q, want \"\"", path)
	}
}

func TestAllowBuildStallDump(t *testing.T) {
	lastBuildStallDumpUnixNano.Store(0)
	now := time.Now()
	if !allowBuildStallDump(now) {
		t.Fatal("first call should be allowed")
	}
	if allowBuildStallDump(now.Add(time.Millisecond)) {
		t.Fatal("second call inside the rate-limit window should be rejected")
	}
	if !allowBuildStallDump(now.Add(buildStallDumpMinInterval + time.Second)) {
		t.Fatal("call after the rate-limit window should be allowed")
	}
}
