// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"sync/atomic"

	"github.com/n42blockchain/N42/params"
)

// profile1Chain records that the node runs a Profile-1 chain (mixed
// production standard). Set once at start-up, before the sync service serves.
var profile1Chain atomic.Bool

// ConfigureProfile1 records whether cfg is a Profile-1 chain; the sync
// servers read it for the rules that differ on the profile (standard §3).
func ConfigureProfile1(cfg *params.ChainConfig) {
	profile1Chain.Store(cfg.IsProfile1())
}

// blockByHashMissCode is the result code of a block_by_hash miss: one
// encoding, 0x03 RESOURCE_UNAVAILABLE, on Profile 1 (standard §3, closes
// SYNC B9); the legacy 0x02 + "block not found" elsewhere.
func blockByHashMissCode() byte {
	if profile1Chain.Load() {
		return responseCodeResourceUnavailable
	}
	return responseCodeServerError
}
