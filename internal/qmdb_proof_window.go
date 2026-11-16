// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

// DefaultQMDBUndoWindow is the number of recent blocks whose QMDB undo records
// are kept: the reach of the recent-heights eth_getProof window and of the
// HotStuff sibling-switch unwind.
const DefaultQMDBUndoWindow = 256

// QMDBUndoWindow returns the configured undo window (blocks).
func QMDBUndoWindow() uint64 { return DefaultQMDBUndoWindow }
