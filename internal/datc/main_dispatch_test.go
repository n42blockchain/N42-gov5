// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// main_dispatch_test.go covers Main()'s subcommand dispatch (main.go:115):
// every known subcommand name routes to its own run* worker (which then
// dies on its own required-flag check, since no flags are supplied), and an
// unrecognized first argument or no argument at all prints the top-level
// usage string.
package datc

import (
	"os"
	"testing"
)

// mainDispatchHelper re-execs with DATC_HELPER=maindispatch and
// DATC_HELPER_ARGS holding the CLI args (space-separated; the tested
// commands take no args with spaces in them) to call.
func TestMainDispatchHelper(t *testing.T) {
	if os.Getenv("DATC_HELPER") != "maindispatch" {
		t.Skip("run via the Main-dispatch subtests")
	}
	argv := []string{"n42-datc"}
	if raw := os.Getenv("DATC_HELPER_ARGS"); raw != "" {
		argv = append(argv, splitFields(raw)...)
	}
	os.Args = argv
	Main()
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func TestMainDispatchKnownSubcommandsDieOnMissingFlags(t *testing.T) {
	// Every subcommand Main() recognizes, given no further flags: each one's
	// own run* worker requires at least --out (or an equivalent) and calls
	// die(), so the process exits 1. finalize-leaves and build are handled
	// inline in Main() itself rather than via a run* dispatch function, but
	// behave the same way.
	for _, name := range []string{
		"verify", "diag", "folddiff", "stor", "segexport", "proof", "bench",
		"prep-state", "set-start", "merge", "segcount", "stamp-meta", "reframe",
		"derive-ns", "slim", "serving-copy", "weekly", "derive-plan", "verify-ns",
		"derive-acc-parts", "bench-plan", "finalize-leaves", "build",
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runHelperSubprocess(t, "^TestMainDispatchHelper$", "DATC_HELPER=maindispatch", "DATC_HELPER_ARGS="+name)
			if code != 1 {
				t.Fatalf("subcommand %q: expected exit 1, got %d (output: %s)", name, code, out)
			}
		})
	}
}

func TestMainDispatchNoArgsUsage(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestMainDispatchHelper$", "DATC_HELPER=maindispatch")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "usage: n42-datc") {
		t.Fatalf("expected the usage string, got %q", out)
	}
}

func TestMainDispatchUnknownSubcommandUsage(t *testing.T) {
	code, out := runHelperSubprocess(t, "^TestMainDispatchHelper$", "DATC_HELPER=maindispatch", "DATC_HELPER_ARGS=bogus-command")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (output: %s)", code, out)
	}
	if !contains(out, "usage: n42-datc") {
		t.Fatalf("expected the usage string, got %q", out)
	}
}
