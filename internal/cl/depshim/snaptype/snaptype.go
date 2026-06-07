//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Package snaptype is a depshim for erigon's db/snaptype — it provides only the
// constants the Caplin antiquary references. N42 does not use erigon's snapshot
// type registry (DB-fallback model); the merge limit governs the slot-range
// granularity at which the antiquary would group state into snapshot files.

package snaptype

// CaplinMergeLimit is the slot-range granularity for Caplin snapshot merges
// (erigon db/snaptype.CaplinMergeLimit).
const CaplinMergeLimit = 10_000
