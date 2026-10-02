package modestate

import (
	"strings"
	"testing"

	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
)

// TestRetentionString covers every named Retention value plus the default "?"
// fallback for an out-of-range value.
func TestRetentionString(t *testing.T) {
	cases := map[Retention]string{
		Keep:          "keep",
		Drop:          "drop-all",
		DropBefore:    "drop-before",
		ColdOffload:   "cold-offload",
		Retention(99): "?",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Errorf("Retention(%d).String() = %q, want %q", r, got, want)
		}
	}
}

// TestPlanString checks the rendered dry-run plan includes the mode name, the
// action counts, and formats both cutoff and non-cutoff lines.
func TestPlanString(t *testing.T) {
	const tip, merge, window = 25_000_000, 15_537_394, 100_000
	plan := PrunePlan(rpccaps.Full, tip, merge, window)
	s := PlanString(rpccaps.Full, plan)

	if !strings.Contains(s, "prune plan for mode") {
		t.Errorf("missing header: %q", s)
	}
	if !strings.Contains(s, rpccaps.Full.String()) {
		t.Errorf("missing mode name: %q", s)
	}
	// AllBodies is ColdOffload with a cutoff -> rendered with "<".
	if !strings.Contains(s, "cold-offload") || !strings.Contains(s, "<") {
		t.Errorf("expected a cutoff-formatted line, got %q", s)
	}
	// Some class should render without a cutoff (plain Drop, no "<").
	found := false
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "drop-all") && !strings.Contains(line, "<") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a non-cutoff drop-all line, got %q", s)
	}
}
