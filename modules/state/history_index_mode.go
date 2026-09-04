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
//
// The refusal is not a permanent capability loss, and this is the point that
// makes the flag reasonable rather than merely fast. The index is a PURE
// FUNCTION OF THE CHANGESETS: WriteHistory reads the same account/storage
// changes that WriteChangeSets persists and emits (key -> block numbers)
// bitmaps. Nothing else feeds it. So it can be produced out of band from
// changesets that are being kept anyway, and the tooling already exists --
// cmd/n42-hist-from-freezer builds accthist/storhist segments directly from
// the acctcs/storcs freezer, and says in its own header that it exists so the
// full/archive tiers can ship historical indexes without an Erigon source.
//
// The right mental model is therefore: changesets are the durable INPUT, the
// history index is a DERIVED CACHE. Building the cache inline, in the block
// commit path, is a choice; on a node that never serves historical queries it
// is 20.3% of the write path spent on a cache with no reader, and it can be
// rebuilt later for the cost of one offline pass.
var (
	historyIndexOnce     sync.Once
	historyIndexDisabled bool
	historyDeferredOnce  sync.Once
	historyDeferred      bool
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

// HistoryIndexDeferred reports whether index maintenance runs OFF the block
// commit path -- skipped inline, rebuilt from the changesets by a background
// backfiller. Composes with the flag above rather than replacing it: both skip
// the inline write, and this one additionally keeps the capability, because
// the index is a pure function of the changesets and they are already durable
// when the block commits.
//
// The difference that matters to a caller is the interlock. With the index
// simply OFF, historical queries are refused outright. Deferred, they are
// refused only ABOVE the backfill marker: below it the index is complete and
// the answers are correct. An index known to be behind is safe; one silently
// behind is not, because HistoricalStateReader reads a missing entry as
// "untouched" and falls back to the current value.
func HistoryIndexDeferred() bool {
	historyDeferredOnce.Do(func() {
		historyDeferred = parseHistoryIndexDisabled(os.Getenv("N42_HISTORY_INDEX_DEFERRED"))
	})
	return historyDeferred
}
