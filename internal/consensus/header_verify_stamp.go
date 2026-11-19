// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package consensus

import (
	"sync/atomic"
	"time"
)

// HeaderVerifySplit is the split of the most recent header verification an
// engine stamped (r97a stamps; HotStuff's VerifyHeader on the import path):
// the parent lookup, the gas-used bound's ancestor walk, the QC check, the
// committee-evidence link, the whole call, and how the QC check was answered
// (QCCache: "off", "hit", "miss", "check", or "" when there was no QC to
// check).
type HeaderVerifySplit struct {
	Number                   uint64
	Parent, Gas, QC, CE, All time.Duration
	QCCache                  string
}

var lastHeaderVerify atomic.Pointer[HeaderVerifySplit]

// NoteHeaderVerify records s as the most recent header verification.
func NoteHeaderVerify(s HeaderVerifySplit) { lastHeaderVerify.Store(&s) }

// LastHeaderVerify returns the most recent header verification's split
// (zero before the first).
func LastHeaderVerify() HeaderVerifySplit {
	if s := lastHeaderVerify.Load(); s != nil {
		return *s
	}
	return HeaderVerifySplit{}
}
