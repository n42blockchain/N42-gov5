// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// verifyns_run_test.go covers runVerifyNS's CLI wrapper against the shared
// run-worker fixture archive (which carries an exact ladder, built via
// derive-ns in buildFixtureArchive), and its --out-required die() path.
package datc

import (
	"os"
	"testing"
)

func TestRunVerifyNSHappyPath(t *testing.T) {
	f := newRunFixture(t)
	out := captureStdout(t, func() {
		runVerifyNS([]string{"--out", f.archiveDir, "--contracts", "10", "--per-contract", "4"})
	})
	if !contains(out, "verify-ns:") {
		t.Fatalf("expected a verify-ns summary line, got %q", out)
	}
}

func TestRunVerifyNSHelper(t *testing.T) {
	if os.Getenv("DATC_HELPER") != "verifyns-noout" {
		t.Skip("run via TestRunVerifyNSMissingOutExits")
	}
	runVerifyNS(nil)
}

func TestRunVerifyNSMissingOutExits(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestRunVerifyNSHelper$", "DATC_HELPER=verifyns-noout")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "--out required") {
		t.Fatalf("expected --out required message, got %q", out)
	}
}
