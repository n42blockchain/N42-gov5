// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// segexport_run_test.go covers runSegExport's CLI wrapper: the happy path
// (against the shared run-worker fixture archive, optionally writing segment
// files to --write-dir) and the --out-required die() path.
package datc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunSegExportHappyPath(t *testing.T) {
	f := newRunFixture(t)
	writeDir := filepath.Join(t.TempDir(), "segs")
	out := captureStdout(t, func() {
		runSegExport([]string{"--out", f.archiveDir, "--write-dir", writeDir, "--map.gb", "4"})
	})
	if !contains(out, "TOTAL raw-stream") {
		t.Fatalf("expected summary line in output, got %q", out)
	}
	entries, err := os.ReadDir(writeDir)
	if err != nil {
		t.Fatalf("expected write-dir to be created: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one .seg.zst file written")
	}
}

func TestRunSegExportHelper(t *testing.T) {
	if os.Getenv("DATC_HELPER") != "segexport-noout" {
		t.Skip("run via TestRunSegExportMissingOutExits")
	}
	runSegExport(nil)
}

func TestRunSegExportMissingOutExits(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestRunSegExportHelper$", "DATC_HELPER=segexport-noout")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "--out required") {
		t.Fatalf("expected --out required message, got %q", out)
	}
}
