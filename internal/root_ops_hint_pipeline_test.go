// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules/state/commitment"
)

// TestRootOpsHintLagPipeline: with the transfer lanes on, chains of frame
// blocks through the ahead/lag/import pipeline (ordered writer, injected
// write failure and recovery, every block checked against a serial chain's
// plain import) end with the same executed results and the same rows in
// every table whether the lane blocks' roots take their ops from the run's
// hint (N42_ROOT_OPS_FROM_RUN) and build them before the tree lock
// (N42_ROOT_OPS_UNLOCKED) or not, with the root rules on and off and on
// early snapshots; the hint is used on lane blocks and check mode finds no
// difference.
func TestRootOpsHintLagPipeline(t *testing.T) {
	for _, c := range []struct {
		name         string
		rules, early bool
	}{{"rulesOff", false, false}, {"rulesOn", true, false}, {"earlySnapshot", false, true}} {
		t.Run(c.name, func(t *testing.T) {
			ref := runLanesPipeline(t, "on", c.rules, c.early)
			require.GreaterOrEqual(t, ref.used, uint64(8), "the lanes took few blocks")
			for _, m := range []struct{ fromRun, unlocked string }{
				{"on", "off"}, {"off", "on"}, {"on", "on"}, {"check", "check"},
			} {
				func() {
					defer commitment.SetRootOpsFromRunForTest(m.fromRun)()
					defer commitment.SetRootOpsUnlockedForTest(m.unlocked)()
					h0, c0, mm0, late0, bad0 := commitment.RootOpsStats()
					u0, hd0 := commitment.RootOpsUnlockedChecks(), rootHintsHanded.Load()
					got := runLanesPipeline(t, "on", c.rules, c.early)
					h1, c1, mm1, late1, bad1 := commitment.RootOpsStats()
					u1, hd1 := commitment.RootOpsUnlockedChecks(), rootHintsHanded.Load()
					what := "fromRun=" + m.fromRun + " unlocked=" + m.unlocked
					t.Logf("%s: handed %d, lanes %d, hinted %d, checked %d, late %d, unusable %d, unlocked checks %d",
						what, hd1-hd0, got.used, h1-h0, c1-c0, late1-late0, bad1-bad0, u1-u0)
					require.Equal(t, mm0, mm1, "%s: check mode found a difference", what)
					require.Equal(t, bad0, bad1, "%s: unusable hints", what)
					if m.fromRun != "off" {
						// Each lane block hands its state a hint; the root of
						// every one that is imported takes it (once, or twice
						// in the unlocked check mode) unless it was not ready.
						// An ahead run the pipeline discards (the harness'
						// injected failure) hands one nobody takes.
						require.Equal(t, got.used, hd1-hd0, "%s: hints handed", what)
						require.GreaterOrEqual(t, 4*((h1-h0)+(late1-late0)), 3*(hd1-hd0), "%s: hinted roots", what)
						require.NotZero(t, h1-h0, "%s: no root took the hint", what)
					}
					if m.fromRun == "check" {
						require.Equal(t, h1-h0, c1-c0, "%s: check comparisons", what)
					}
					if m.unlocked == "check" {
						// Every hinted root compared its two builds.
						require.Equal(t, 2*(u1-u0), (h1-h0)+(late1-late0), "%s: unlocked checks", what)
					}
					require.Equal(t, ref.results, got.results, "%s: executed results", what)
					requireSameTables(t, ref.tables, got.tables, what)
				}()
			}
		})
	}
}
