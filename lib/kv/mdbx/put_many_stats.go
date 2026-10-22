package mdbx

import "sync/atomic"

var sameValueBatches, sameValueAttempts, sameValueWritten atomic.Uint64

// SameValueBatchStats counts completed native batches and their attempted and
// successful rows. These are process-lifetime counters, not transaction counts.
func SameValueBatchStats() (batches, attempted, written uint64) {
	return sameValueBatches.Load(), sameValueAttempts.Load(), sameValueWritten.Load()
}
