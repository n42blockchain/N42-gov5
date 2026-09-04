package state

import (
	"os"
	"sync"
)

// N42_NO_HISTORY_INDEX makes a node skip the AccountHistory/StorageHistory
// inverted index. Off by default; the env-var idiom matches N42_MDBX_SYNC and
// N42_MDBX_MAPSIZE_GB, and it is deliberately not a config field because it
// removes a query capability and should have to be typed out.
//
// Why it exists. Measured on the qs fleet at 22,857 transactions a block:
// WriteHistory is 117.6 ms against WriteChangeSets' 8.7 ms -- 92.7% of the
// chgset phase and 20.3% of the whole write path. The index is a
// read-modify-write of a roaring bitmap per changed key per block, ~24k keys a
// block, and a throughput workload never reads it back.
//
// What it does NOT disable: changesets. Those are load-bearing three ways --
// PlainState's only rewind source on the native chain (realignAppliedToTree),
// eth-el's separate path, and the DATC archive's only input -- and they cost
// 8.7 ms. See docs/QS_WRITE_PATH_RETENTION.md. The expensive half has one
// consumer; the cheap half has three.
//
// The interlock matters more than the saving. With the index incomplete,
// historical state queries do not fail, they answer from whatever PlainState
// currently holds -- a wrong answer delivered confidently, which is worse than
// a slow chain. So api.State refuses historical queries when this is set,
// following the sealed-horizon gate that already refuses rather than lying.
var (
	historyIndexOnce     sync.Once
	historyIndexDisabled bool
)

// HistoryIndexDisabled reports whether this node skips the history index.
// Read once: the answer must not change under a running node, or the index
// would have holes no reader could detect.
func HistoryIndexDisabled() bool {
	historyIndexOnce.Do(func() {
		historyIndexDisabled = parseHistoryIndexDisabled(os.Getenv("N42_NO_HISTORY_INDEX"))
	})
	return historyIndexDisabled
}

// parseHistoryIndexDisabled is split out because the sync.Once above latches on
// first use, which is correct for a node and untestable in a package where
// another test may have called it first. Anything not explicitly affirmative
// leaves the index ON: a typo must not silently disable a query capability.
func parseHistoryIndexDisabled(v string) bool {
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	}
	return false
}
