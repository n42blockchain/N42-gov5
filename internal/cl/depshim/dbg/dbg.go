// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Dbg unit for the dbg package.
// Debug helper shims.
// Part of the n42el consensus-layer build.

//go:build n42el

// Package dbg re-exports the tiny subset of erigon/common/dbg that the cl/
// tree references. Both Stack and the deadlock-detection toggle are
// satisfied by N42's lib/common/dbg.

package dbg

import libdbg "github.com/n42blockchain/N42/lib/common/dbg"

// Stack returns the current goroutine stack as a string.
var Stack = libdbg.Stack

// ReadMemStats reads runtime memory statistics (used by Caplin's stage loop for
// periodic memory logging).
var ReadMemStats = libdbg.ReadMemStats

// CaplinSyncedDataMangerDeadlockDetection toggles a deadlock-detection
// goroutine in Caplin's synced-data manager. The N42 fork keeps it disabled
// — flipping it would only add background work for a feature we do not need
// in production.
const CaplinSyncedDataMangerDeadlockDetection = false

// AssertEnabled toggles invariant checks in upstream code. Off in the
// N42 fork — production paths should not pay for runtime asserts.
const AssertEnabled = false

// TraceDeletion toggles per-file delete logging in common/dir.
// Off in the N42 fork.
const TraceDeletion = false
