package modestate

import (
	"testing"

	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
)

func findAction(plan []PruneAction, c rpccaps.DataClass) (PruneAction, bool) {
	for _, a := range plan {
		if a.Class == c {
			return a, true
		}
	}
	return PruneAction{}, false
}

// TestFullPrunePlan asserts the data-trimming for the asked-for "full" profile:
// drop pre-merge bodies (keep post-merge), drop historical state (keep latest),
// drop receipts older than the recent window (keep recent), drop the M0 changeset.
func TestFullPrunePlan(t *testing.T) {
	const tip, merge, window = 25_000_000, 15_537_394, 100_000
	plan := PrunePlan(rpccaps.Full, tip, merge, window)

	// EIP-4444 standard: bodies cold-offloaded below the ~1yr window (tip-window),
	// not dropped at the merge boundary.
	if a, ok := findAction(plan, rpccaps.AllBodies); !ok || a.Action != ColdOffload || a.Cutoff != tip-window {
		t.Errorf("AllBodies: want ColdOffload@%d (EIP-4444 window), got %+v (ok=%v)", tip-window, a, ok)
	}
	// Historical state dropped entirely (latest kept).
	if a, ok := findAction(plan, rpccaps.HistoricalState); !ok || a.Action != Drop {
		t.Errorf("HistoricalState: want Drop, got %+v (ok=%v)", a, ok)
	}
	// Old receipts dropped before tip-window.
	if a, ok := findAction(plan, rpccaps.AllReceipts); !ok || a.Action != DropBefore || a.Cutoff != tip-window {
		t.Errorf("AllReceipts: want DropBefore@%d, got %+v (ok=%v)", tip-window, a, ok)
	}
	// Rolling changeset (M0-only) dropped.
	if a, ok := findAction(plan, rpccaps.RollingChangeset); !ok || a.Action != Drop {
		t.Errorf("RollingChangeset: want Drop, got %+v (ok=%v)", a, ok)
	}
	// Kept classes must NOT appear in the plan.
	for _, kept := range []rpccaps.DataClass{rpccaps.LatestState, rpccaps.PostMergeBodies,
		rpccaps.RecentReceipts, rpccaps.TxHashIndex, rpccaps.LogIndex, rpccaps.Headers} {
		if _, ok := findAction(plan, kept); ok {
			t.Errorf("%v is required by Full and must not be pruned", kept)
		}
	}
}

// TestArchivePrunesNothing: archive keeps everything.
func TestArchivePrunesNothing(t *testing.T) {
	if plan := PrunePlan(rpccaps.Archive, 25_000_000, 15_537_394, 100_000); len(plan) != 0 {
		t.Errorf("archive should prune nothing, got %d actions:\n%s",
			len(plan), PlanString(rpccaps.Archive, plan))
	}
}

// TestM1PrunesBodiesAndReceipts: M1 keeps latest state only — no bodies (drop
// all), no receipts (drop all), no historical state.
func TestM1PrunePlan(t *testing.T) {
	plan := PrunePlan(rpccaps.M1, 25_000_000, 15_537_394, 100_000)
	if a, ok := findAction(plan, rpccaps.AllBodies); !ok || a.Action != Drop {
		t.Errorf("M1 AllBodies: want Drop (no body queries), got %+v (ok=%v)", a, ok)
	}
	if a, ok := findAction(plan, rpccaps.AllReceipts); !ok || a.Action != Drop {
		t.Errorf("M1 AllReceipts: want Drop, got %+v (ok=%v)", a, ok)
	}
	if _, ok := findAction(plan, rpccaps.LatestState); ok {
		t.Error("M1 must keep LatestState")
	}
}
