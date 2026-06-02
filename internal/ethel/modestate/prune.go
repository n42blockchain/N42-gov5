package modestate

import (
	"fmt"

	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
)

// Retention is the action for one data class under a mode's prune policy.
type Retention int

const (
	Keep       Retention = iota // retain in full (required by the mode)
	Drop                        // remove entirely (mode never needs it)
	DropBefore                  // remove blocks strictly below Cutoff (keep the recent/post-merge tail)
)

func (r Retention) String() string {
	switch r {
	case Keep:
		return "keep"
	case Drop:
		return "drop-all"
	case DropBefore:
		return "drop-before"
	default:
		return "?"
	}
}

// PruneAction is one entry of a dry-run prune plan. It is descriptive only — no
// deletion happens here; an executor (future, operating on the real chaindata +
// freezers) consumes the plan. Cutoff is meaningful only for DropBefore.
type PruneAction struct {
	Class     rpccaps.DataClass
	Action    Retention
	Cutoff    uint64
	Note      string
	StoreHint string // concrete N42 store the executor would touch
}

// storeHint maps a data class to the N42 store an executor would prune. Loose by
// design — the executor binds exact freezer/table names; this documents intent.
var storeHint = map[rpccaps.DataClass]string{
	rpccaps.PostMergeBodies:  "bodyc freezer",
	rpccaps.AllBodies:        "bodyc freezer (pre-merge segments)",
	rpccaps.TxHashIndex:      "tx-lookup index",
	rpccaps.HistoricalState:  "history domain (lib/state) + per-block trie roots",
	rpccaps.AllReceipts:      "receipts freezer + log index",
	rpccaps.RecentReceipts:   "receipts freezer (recent window)",
	rpccaps.LogIndex:         "log address/topic index",
	rpccaps.RollingChangeset: "acctcs/storcs window",
	rpccaps.LatestState:      "PlainState + commitment",
	rpccaps.Headers:          "headerc freezer",
}

// PrunePlan returns the dry-run prune plan for a mode: for every prunable data
// class, the action (Drop entirely, or DropBefore a cutoff when the mode keeps a
// recent/post-merge tail of the same data). tip is the current head; mergeBlock
// is the PoS merge height (pre-merge bodies are droppable when only post-merge tx
// queries are served); recentWindow is how many recent blocks of receipts/logs to
// keep. Required classes are not listed (they are kept).
func PrunePlan(mode rpccaps.Mode, tip, mergeBlock, recentWindow uint64) []PruneAction {
	keep := map[rpccaps.DataClass]bool{}
	for _, d := range rpccaps.RequiredData(mode) {
		keep[d] = true
	}
	receiptCutoff := uint64(0)
	if tip > recentWindow {
		receiptCutoff = tip - recentWindow
	}
	var plan []PruneAction
	for _, c := range rpccaps.Prunable(mode) {
		act := PruneAction{Class: c, Action: Drop, StoreHint: storeHint[c]}
		switch c {
		case rpccaps.AllBodies:
			// If the mode keeps post-merge bodies, only pre-merge bodies are droppable.
			if keep[rpccaps.PostMergeBodies] {
				act.Action, act.Cutoff = DropBefore, mergeBlock
				act.Note = "keep post-merge bodies; drop pre-merge"
			} else {
				act.Note = "no body queries in this mode"
			}
		case rpccaps.AllReceipts:
			if keep[rpccaps.RecentReceipts] {
				act.Action, act.Cutoff = DropBefore, receiptCutoff
				act.Note = "keep recent receipts/logs window; drop older"
			} else {
				act.Note = "no receipt/log queries in this mode"
			}
		case rpccaps.HistoricalState:
			act.Note = "keep LatestState only (no state-as-of history)"
		default:
			act.Note = "not needed in this mode"
		}
		plan = append(plan, act)
	}
	return plan
}

// String renders a plan for logging / a --prune dry-run command.
func PlanString(mode rpccaps.Mode, plan []PruneAction) string {
	s := fmt.Sprintf("prune plan for mode %s (%d actions):\n", mode, len(plan))
	for _, a := range plan {
		if a.Action == DropBefore {
			s += fmt.Sprintf("  %-18s %s < %d  [%s] — %s\n", a.Class, a.Action, a.Cutoff, a.StoreHint, a.Note)
		} else {
			s += fmt.Sprintf("  %-18s %s        [%s] — %s\n", a.Class, a.Action, a.StoreHint, a.Note)
		}
	}
	return s
}
