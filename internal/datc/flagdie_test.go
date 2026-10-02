// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Flag-validation die() paths for several run* entry points. Each worker is
// invoked with args missing a required flag; the subprocess pattern lets the
// test assert the exit code and stderr message without os.Exit tearing down
// the real test binary.

package datc

import (
	"os"
	"testing"
)

func TestFlagDieHelper(t *testing.T) {
	which := os.Getenv("DATC_HELPER")
	switch which {
	case "reframe-noout":
		runReframe(nil)
	case "setstart-noforce":
		runSetStart([]string{"-out", t.TempDir()})
	case "benchplan-nojson":
		runBenchPlan([]string{"-out", t.TempDir(), "-contracts", "x"})
	default:
		t.Skip("run via the matching TestXxxExits")
	}
}

func TestRunReframeMissingOutExits(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestFlagDieHelper$", "DATC_HELPER=reframe-noout")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "--out required") {
		t.Fatalf("expected --out required message, got %q", out)
	}
}

func TestRunSetStartMissingForceExits(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestFlagDieHelper$", "DATC_HELPER=setstart-noforce")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "--out and --force required") {
		t.Fatalf("expected --out and --force required message, got %q", out)
	}
}

func TestRunBenchPlanMissingJSONExits(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestFlagDieHelper$", "DATC_HELPER=benchplan-nojson")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "--out, --contracts and --json required") {
		t.Fatalf("expected the combined required-flags message, got %q", out)
	}
}
