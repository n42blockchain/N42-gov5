// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"os"
	"sync/atomic"
)

// Two levers on the serial import cycle of the lag pipeline (S126: a full
// 163k block cycles in ~123 ms with the block interval at 100 ms). The
// cycle is the chain publish(N) -> ahead run of N+1 (prep wait, setup,
// Block-STM, collect) -> import of N+1 (block end, rewards, root walk) ->
// publish(N+1); the import goroutine idles in aheadWait meanwhile.
//
// N42_LAG_EARLY_PUBLISH=1 (lever a). What the child's ahead run reads is
// its parent's post-state: the root's dirty set, built by the root walk
// (IntraBlockState.computeRootViaComputer) from the parent's state after
// the fold of its Block-STM writes (applyMVSToIBS), the deferred fee
// credits, the block-end system calls and the rewards. Every one of those
// steps writes state the child reads (the fold writes every account the
// block touched, the fees credit the fee recipients, the system calls write
// their contracts), so no post-state exists before collect ends and the
// parent's import has run its block end: publishing before collect would
// hand the child a different state. The part of collect that writes no
// state is the receipts assembly (receipts, logs, cumulative gas, fee
// totals; ~5 ms of collect's ~22 ms at 163k): with the switch on, an ahead
// run assembles it on its own goroutines while the fold runs, and credits
// the fees once both are done -- the same receipts, the same state, the
// same order of state writes. The post-state is then complete, and
// published, that much earlier.
//
// N42_AHEAD_PREP_EARLY=1 (lever b). A queued child's preparation (its
// messages, hashes and affinity keys, ahead_prep.go) and its base prefill
// (ahead_base_prefill.go) ran one after the other: the prefill started only
// once the preparation was computed, and a child queued less than ~45 ms
// before its parent published waited for both (aheadPre). With the switch
// on, the prefill starts with the preparation, its keys taken from the
// transactions (the senders are settled by then, and the preparation is
// not read), and a third queued block is prepared too (aheadPrepCap 3).
// The prefill's correctness does not depend on when it starts: it reads
// the tree only in the state of one of the child's ancestors within the
// depth, checks afterwards that only the roots of the child's ancestors ran
// meanwhile, and every key an ancestor above that state changed is answered
// by that ancestor's post-state layered on the child's reader -- a key the
// parent changes is never read from the prefill.

// lagEarlyPublishOn is N42_LAG_EARLY_PUBLISH (default off; "1", "true" or
// "on" enables).
var lagEarlyPublishOn = parseOptIn(os.Getenv("N42_LAG_EARLY_PUBLISH"))

// aheadPrepEarlyOn is N42_AHEAD_PREP_EARLY (default off; "1", "true" or
// "on" enables).
var aheadPrepEarlyOn = parseOptIn(os.Getenv("N42_AHEAD_PREP_EARLY"))

func parseOptIn(v string) bool {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return true
	}
	return false
}

// SetLagEarlyPublishForTest switches lever a and returns the restore.
func SetLagEarlyPublishForTest(on bool) func() {
	prev := lagEarlyPublishOn
	lagEarlyPublishOn = on
	return func() { lagEarlyPublishOn = prev }
}

// SetAheadPrepEarlyForTest switches lever b and returns the restore.
func SetAheadPrepEarlyForTest(on bool) func() {
	prev := aheadPrepEarlyOn
	aheadPrepEarlyOn = on
	return func() { aheadPrepEarlyOn = prev }
}

// lagRcptOverlaps counts the ahead runs that assembled their receipts
// beside the fold (since start).
var lagRcptOverlaps atomic.Uint64
