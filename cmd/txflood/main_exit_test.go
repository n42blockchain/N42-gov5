package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestTxfloodMainRejectsNegativeFlags drives main() itself, via the
// subprocess re-exec pattern, down its earliest validation branch, which
// calls os.Exit(2) and so cannot run in-process without killing the test
// binary. TXFLOOD_MAIN_SUBPROCESS guards it so a normal `go test` run never
// executes main().
func TestTxfloodMainRejectsNegativeFlags(t *testing.T) {
	if os.Getenv("TXFLOOD_MAIN_SUBPROCESS") == "1" {
		os.Args = []string{"txflood", "-senders=-1"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestTxfloodMainRejectsNegativeFlags")
	cmd.Env = append(os.Environ(), "TXFLOOD_MAIN_SUBPROCESS=1")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected process to exit with an error, got %v (output: %s)", err, out)
	}
	if code := exitErr.ExitCode(); code != 2 {
		t.Fatalf("exit code = %d, want 2 (output: %s)", code, out)
	}
	if !strings.Contains(string(out), "senders must be non-negative; pertx/count must be positive") {
		t.Fatalf("output = %q, missing validation message", out)
	}
}
