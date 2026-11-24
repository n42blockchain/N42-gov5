// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package syncanchor

import "sync/atomic"

// profile1Enforced pins the mode at Enforce for the life of the process.
var profile1Enforced atomic.Bool

// EnforceForProfile1 fixes the anchor mode at Enforce: on a Profile-1 chain
// anchored ranges are enforced from block 0 and the observe/off modes are
// unreachable (mixed production standard §3). N42_SYNC_ANCHOR with any other
// value than "enforce" is refused at start-up before this runs. A chain
// outside the profile never calls it and keeps the env-selected mode.
func EnforceForProfile1() {
	profile1Enforced.Store(true)
	current.Store(int32(Enforce))
}
