// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import "sync/atomic"

// DefaultQMDBUndoWindow is the number of recent blocks whose QMDB undo records
// are kept: the reach of the recent-heights eth_getProof window and of the
// HotStuff sibling-switch unwind. It is also the minimum: a smaller window
// would shorten the unwind reach the consensus layer relies on.
const DefaultQMDBUndoWindow = 256

var qmdbUndoWindow atomic.Uint64

// SetQMDBUndoWindow configures the undo window (0 = default; values below
// the default are raised to it). Returns the effective window. Set once at
// startup, before blocks are processed.
func SetQMDBUndoWindow(n uint64) uint64 {
	if n < DefaultQMDBUndoWindow {
		n = DefaultQMDBUndoWindow
	}
	qmdbUndoWindow.Store(n)
	return n
}

// QMDBUndoWindow returns the configured undo window (blocks).
func QMDBUndoWindow() uint64 {
	if n := qmdbUndoWindow.Load(); n != 0 {
		return n
	}
	return DefaultQMDBUndoWindow
}
