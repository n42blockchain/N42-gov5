// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func finalizeWithWorkers(t *testing.T, dir string, workers, runBytes int) {
	t.Helper()
	saved := finalizeWorkers
	finalizeWorkers = workers
	defer func() { finalizeWorkers = saved }()
	finalizeWithRuns(t, dir, runBytes)
}

// TestFinalizeParallelMatchesSerial finalizes the same spill — many buckets,
// a kill-tail frame, duplicate keys, external-sort runs, and a second spill
// merged into the existing segments — with one worker and with several. The
// segment trees must be byte-identical and the kill-tail spill retained.
func TestFinalizeParallelMatchesSerial(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	spillRows(t, src, 7, 12000)
	killed := filepath.Join(src, leafSpillDir, segFileName(leafTableS, 1)+".zspill")
	appendKillTail(t, killed)
	spillRows(t, src, 11, 3000)

	serial := filepath.Join(base, "serial")
	par := filepath.Join(base, "par")
	copyTree(t, src, serial)
	copyTree(t, src, par)
	finalizeWithWorkers(t, serial, 1, 64<<10)
	finalizeWithWorkers(t, par, 4, 64<<10)
	if _, err := os.Stat(filepath.Join(par, leafSpillDir, filepath.Base(killed))); err != nil {
		t.Fatalf("kill-tail bucket spill not retained under parallel finalize: %v", err)
	}
	requireSameTrees(t, serial, par, "first finalize")

	for _, d := range []string{serial, par} {
		_ = os.RemoveAll(filepath.Join(d, leafSpillDir))
		spillRows(t, d, 13, 5000)
	}
	finalizeWithWorkers(t, serial, 1, 32<<10)
	finalizeWithWorkers(t, par, 4, 32<<10)
	if _, err := os.Stat(filepath.Join(par, leafSpillDir)); !os.IsNotExist(err) {
		t.Fatalf("clean spill dir not removed after parallel finalize: %v", err)
	}
	requireSameTrees(t, serial, par, "merge into existing segments")
}
