// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"os"
	"sync/atomic"
)

// N42_LAG_EARLY_PUBLISH=1 (ported from qs/r38a-lag-early). What a child's
// ahead run reads is its parent's post-state: the root's dirty set, built
// by the root walk (IntraBlockState.computeRootViaComputer) from the
// parent's state after the fold of its Block-STM writes (applyMVSToIBS),
// the deferred fee credits, the block-end system calls and the rewards.
// Every one of those steps writes state the child reads, so no post-state
// exists before collect ends. The part of collect that writes no state is
// the receipts assembly (receipts, logs, cumulative gas, fee totals): with
// the switch on, an ahead run assembles it on its own goroutine while the
// fold runs (including the N42_COLLECT_SHARD bulk pass) and credits the
// fees once both are done -- the same receipts, the same state, the same
// order of state writes. The post-state is then complete, and published,
// that much earlier.

// lagEarlyPublishOn is N42_LAG_EARLY_PUBLISH (default off; "1", "true" or
// "on" enables).
var lagEarlyPublishOn = parseOptIn(os.Getenv("N42_LAG_EARLY_PUBLISH"))

func parseOptIn(v string) bool {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return true
	}
	return false
}

// SetLagEarlyPublishForTest switches N42_LAG_EARLY_PUBLISH and returns the
// restore.
func SetLagEarlyPublishForTest(on bool) func() {
	prev := lagEarlyPublishOn
	lagEarlyPublishOn = on
	return func() { lagEarlyPublishOn = prev }
}

// lagRcptOverlaps counts the ahead runs that assembled their receipts
// beside the fold.
var lagRcptOverlaps atomic.Uint64
