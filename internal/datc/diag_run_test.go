// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// diag_run_test.go covers runDiag end to end against the fixture's
// acctcs/storcs changeset freezer.
package datc

import (
	"strconv"
	"strings"
	"testing"
)

func TestRunDiagHappyPath(t *testing.T) {
	f := newRunFixture(t)
	// Block 0 (genesis) always has a non-empty acctcs blob in the scenario
	// (every EOA/contract is created there).
	out := captureStdout(t, func() {
		runDiag([]string{"--changesets", f.csDir, "--block", "0"})
	})
	if !strings.Contains(out, "acctcs") || !strings.Contains(out, "ACCT") {
		t.Fatalf("unexpected diag output: %s", out)
	}
}

// TestRunDiagStorageBlock finds a block with storage churn (not every block
// in the scenario touches storage) and checks the STOR lines are printed.
func TestRunDiagStorageBlock(t *testing.T) {
	f := newRunFixture(t)
	n, found := 0, false
	for i, gb := range f.sc.blocks {
		if len(gb.slots) > 0 {
			n, found = i, true
			break
		}
	}
	if !found {
		t.Skip("no block with storage changes found in the scenario")
	}
	out := captureStdout(t, func() {
		runDiag([]string{"--changesets", f.csDir, "--block", strconv.Itoa(n)})
	})
	if !strings.Contains(out, "STOR") {
		t.Fatalf("expected STOR lines for block %d, got: %s", n, out)
	}
}
