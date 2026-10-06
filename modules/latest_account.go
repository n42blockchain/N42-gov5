// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The one seam through which every LATEST read of the `Account` table passes.
//
// Round 19's write probe (docs/QS_BLOCK_TIME_BUDGET.md, 6f) measured `Account`
// -- address-keyed, uniformly distributed, one 4 KB page per row -- at 88% of
// the bytes MDBX dirties per full block, and showed it is a duplicate of the
// QMDB entry log, which already holds the same values in the same encoding.
// Round 21 proved the QMDB read equivalent (zero mismatches in a million
// comparisons). Stopping the plain write is the next step, and it is only
// sound if nothing reads the table for the head state any more.
//
// Readers therefore call ReadLatestAccount instead of GetOne(Account, addr).
// With no source installed it IS that call. When a source is installed (the
// node does so under N42_STATE_READ_QMDB=1, after checking the chain commits
// with QMDB), the head account state comes from the source; under
// N42_STATE_WRITE_QMDB_ONLY=1 the plain writer additionally stops maintaining
// the table for blocks after genesis. Historical reads are unaffected: the changesets still carry
// every pre-image, and the "unchanged since block N" fallback is exactly the
// latest value, which the source serves.
//
// Deliberately NOT routed through here: enumerations of the table (snap sync
// serving, state dumps, rebuild tools). Under the flag they see a table frozen
// at the block the flag was first enabled, which is why the flag is a
// measurement lever and not a default. The package doc of the source
// implementation lists them.

package modules

import (
	"sync/atomic"

	"github.com/n42blockchain/N42/lib/kv"
)

// AccountSource answers head-state account reads by address. The getter is
// the caller's own transaction when it has one (nil otherwise); a source may
// read persisted rows through it and must tolerate it lagging the head.
type AccountSource interface {
	LatestAccount(g kv.Getter, addr []byte) (enc []byte, err error)
}

var (
	latestAccountSource atomic.Pointer[AccountSource]
	plainAccountSkipped atomic.Bool
)

// SetLatestAccountSource installs (or, with nil, removes) the head-state
// account source. The node installs one whenever head reads are served from
// the QMDB tree (N42_STATE_READ_QMDB=1), so that EVERY reader -- the miner's
// build, the txpool, RPC -- sees the same state as block execution. That
// invariant is not optional: round 26's first attempt installed the source
// only when the write was also switched off, and the first leg that read
// through the tree while the miner still read the table wedged the fleet on a
// state-root mismatch within one block.
func SetLatestAccountSource(s AccountSource) {
	if s == nil {
		latestAccountSource.Store(nil)
		return
	}
	latestAccountSource.Store(&s)
}

// LatestAccountSourceInstalled reports whether head reads bypass the table.
func LatestAccountSourceInstalled() bool { return latestAccountSource.Load() != nil }

// SetPlainAccountWriteSkipped switches the plain writer off for the `Account`
// table on every block after genesis (N42_STATE_WRITE_QMDB_ONLY=1). Only
// sound with a source installed; the node enforces the pairing.
func SetPlainAccountWriteSkipped(skip bool) { plainAccountSkipped.Store(skip) }

// PlainAccountWriteSkipped reports whether the `Account` table is no longer
// maintained for post-genesis blocks.
func PlainAccountWriteSkipped() bool { return plainAccountSkipped.Load() }

// ReadLatestAccount returns the encoded head-state account for addr, or an
// empty slice when the account does not exist. It is GetOne(Account, addr)
// unless a source is installed.
func ReadLatestAccount(g kv.Getter, addr []byte) ([]byte, error) {
	if p := latestAccountSource.Load(); p != nil {
		return (*p).LatestAccount(g, addr)
	}
	return g.GetOne(Account, addr)
}
