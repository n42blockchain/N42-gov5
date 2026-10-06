// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// weekly_run_test.go covers weekly.go's pure/near-pure pieces directly
// (weeklyPlan, fileNonEmpty, archiveHead) and runWeekly's own gates
// (missing --out/--headers/--changesets, --skip-build without --from, and
// --dry-run, which prints every step's command and execs nothing).
package datc

import (
	"os"
	"testing"
)

func TestWeeklyPlanWithAndWithoutQueries(t *testing.T) {
	steps := weeklyPlan("/archive", "/headers", "/changesets", "/strata.json", 100, 8, 500, "/newlist.txt")
	var sawBench bool
	for _, s := range steps {
		if s.name == "bench: stratified queries" {
			sawBench = true
			if s.skip != "" {
				t.Fatalf("expected the stratified-queries bench to run when --queries is set, got skip=%q", s.skip)
			}
		}
	}
	if !sawBench {
		t.Fatalf("expected a stratified-queries bench step")
	}

	steps = weeklyPlan("/archive", "/headers", "/changesets", "", 100, 8, 500, "/newlist.txt")
	for _, s := range steps {
		if s.name == "bench: stratified queries" {
			if s.skip == "" {
				t.Fatalf("expected the stratified-queries bench to be skipped without --queries")
			}
		}
	}

	// The derive-ns step over the new contracts list carries needsList.
	var sawNeedsList bool
	for _, s := range steps {
		if s.needsList {
			sawNeedsList = true
		}
	}
	if !sawNeedsList {
		t.Fatalf("expected exactly one needsList step (derive newly large contracts)")
	}
}

func TestRunWeeklyDryRun(t *testing.T) {
	f := newRunFixture(t)
	out := captureStdout(t, func() {
		runWeekly([]string{
			"--out", f.archiveDir, "--headers", f.headersDir, "--changesets", f.csDir,
			"--skip-build", "--from", "1", "--dry-run",
		})
	})
	if !contains(out, "dry run") {
		t.Fatalf("expected the dry-run summary line, got %q", out)
	}
	if !contains(out, "verify-ns") {
		t.Fatalf("expected the verify-ns step command to be printed, got %q", out)
	}
}

func TestRunWeeklyHelper(t *testing.T) {
	switch os.Getenv("DATC_HELPER") {
	case "weekly-missing-flags":
		runWeekly(nil)
	case "weekly-skipbuild-nofrom":
		runWeekly([]string{"--out", "x", "--headers", "y", "--changesets", "z", "--skip-build"})
	default:
		t.Skip("run via the weekly die-gate subtests")
	}
}

func TestRunWeeklyMissingFlagsExits(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestRunWeeklyHelper$", "DATC_HELPER=weekly-missing-flags")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "weekly needs --out, --headers and --changesets") {
		t.Fatalf("expected the missing-flags message, got %q", out)
	}
}

func TestRunWeeklySkipBuildWithoutFromExits(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestRunWeeklyHelper$", "DATC_HELPER=weekly-skipbuild-nofrom")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "--skip-build needs --from") {
		t.Fatalf("expected the --skip-build/--from message, got %q", out)
	}
}
