// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// servingcopy_run_test.go covers runServingCopy's CLI wrapper: the happy
// path (hard-link mode against the shared run-worker fixture archive) and
// its die() paths (missing flags, dst already exists).
package datc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunServingCopyHappyPath(t *testing.T) {
	f := newRunFixture(t)
	dst := filepath.Join(t.TempDir(), "serving")
	out := captureStdout(t, func() {
		runServingCopy([]string{"--out", f.archiveDir, "--dst", dst})
	})
	if !contains(out, "serving-copy:") {
		t.Fatalf("expected summary line in output, got %q", out)
	}
	if _, err := os.Stat(filepath.Join(dst, leafSegDir)); err != nil {
		t.Fatalf("expected leafseg dir in the serving copy: %v", err)
	}
}

func TestRunServingCopyDstExists(t *testing.T) {
	f := newRunFixture(t)
	dst := filepath.Join(t.TempDir(), "serving")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATC_HELPER_OUT", f.archiveDir)
	t.Setenv("DATC_HELPER_DST", dst)
	code, out := runHelperSubprocess(t, "^TestRunServingCopyDstExistsHelper$", "DATC_HELPER=servingcopy-dstexists",
		"DATC_HELPER_OUT="+f.archiveDir, "DATC_HELPER_DST="+dst)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "already exists") {
		t.Fatalf("expected 'already exists' message, got %q", out)
	}
}

func TestRunServingCopyDstExistsHelper(t *testing.T) {
	if os.Getenv("DATC_HELPER") != "servingcopy-dstexists" {
		t.Skip("run via TestRunServingCopyDstExists")
	}
	runServingCopy([]string{"--out", os.Getenv("DATC_HELPER_OUT"), "--dst", os.Getenv("DATC_HELPER_DST")})
}

func TestRunServingCopyHelper(t *testing.T) {
	if os.Getenv("DATC_HELPER") != "servingcopy-noflags" {
		t.Skip("run via TestRunServingCopyMissingFlagsExits")
	}
	runServingCopy(nil)
}

func TestRunServingCopyMissingFlagsExits(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestRunServingCopyHelper$", "DATC_HELPER=servingcopy-noflags")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "--out and --dst required") {
		t.Fatalf("expected --out and --dst required message, got %q", out)
	}
}
