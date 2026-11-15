// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package attest

import (
	"os"
	"sync/atomic"
)

// EnvValidatorsCache (N42_ATTEST_VALIDATORS_CACHE=1, default off): the
// node's Validators callback reads the consensus engine's published
// validator set instead of taking the engine's lock on every call (r93: the
// pool asks per frame from Pending, Recovered, Add and InCommittee, and that
// lock queued behind ProcessEvent for ~50% of the node's mutex wait). The
// set is the same pointer the locked read returns; see
// hotstuff.EpochManager.PublishedValidatorSet.
const EnvValidatorsCache = "N42_ATTEST_VALIDATORS_CACHE"

var validatorsCacheOn atomic.Bool

func init() {
	switch os.Getenv(EnvValidatorsCache) {
	case "1", "true", "TRUE", "on", "ON", "yes":
		validatorsCacheOn.Store(true)
	}
}

// ValidatorsCacheOn reports N42_ATTEST_VALIDATORS_CACHE.
func ValidatorsCacheOn() bool { return validatorsCacheOn.Load() }

// SetValidatorsCacheForTest switches N42_ATTEST_VALIDATORS_CACHE and returns
// the restore.
func SetValidatorsCacheForTest(on bool) func() {
	prev := validatorsCacheOn.Swap(on)
	return func() { validatorsCacheOn.Store(prev) }
}
