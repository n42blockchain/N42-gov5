// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// N42_DEFERRED_CHECK_CONCURRENT: a follower's block-push receive path
// dispatches InsertChain immediately after decode and runs
// CheckDeferredBlock concurrently on its own goroutine, instead of running
// the check first and InsertChain only after it returns
// (docs/QS_BLOCK_TIME_BUDGET.md 6dx: the check is 228ms of median WORK,
// not a wait, sitting serially in front of every follower's own 792ms
// InsertChain). Whichever finishes first advances the vote gate through
// the EXISTING notification paths (NotifyBlockImported / NotifyBlockChecked),
// which already treat "imported" as independently sufficient regardless of
// "checked" (deferredAttested's own callers check importedBlocks OR
// deferredAttested) -- no consensus-side change is needed for either
// ordering. Off unless the variable is set -- unset/"0" runs the check
// first and InsertChain after, exactly as today.

package sync

import (
	"os"
	"sync"
)

var (
	deferredCheckConcurrentOnce sync.Once
	deferredCheckConcurrentOn   bool
)

// DeferredCheckConcurrentOn reports whether the block-push receive path
// should dispatch InsertChain immediately and run CheckDeferredBlock
// concurrently, instead of sequentially.
func DeferredCheckConcurrentOn() bool {
	deferredCheckConcurrentOnce.Do(func() {
		deferredCheckConcurrentOn = parseDeferredCheckConcurrent(os.Getenv("N42_DEFERRED_CHECK_CONCURRENT"))
	})
	return deferredCheckConcurrentOn
}

func parseDeferredCheckConcurrent(v string) bool {
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	default:
		return false
	}
}
