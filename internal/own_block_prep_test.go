// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOwnBlockPrep (N42_OWN_BLOCK_PREP): the harness queues every block as a
// leader queues its own sealed blocks. Off, no block is prepared before its
// run; on, every block's preparation starts at the seal and the ahead runs
// adopt it. Each block must equal the serial chain's (the harness), and both
// chains must end with the same rows in every table; with early snapshots
// and the base prefill's cut claims as on the fleet too.
func TestOwnBlockPrep(t *testing.T) {
	for _, fleet := range []bool{false, true} {
		name := "plain"
		if fleet {
			name = "earlySnapshot"
		}
		t.Run(name, func(t *testing.T) {
			defer SetAheadPrepForTest(true)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			if fleet {
				defer SetLagEarlySnapshotForTest(true)()
				defer SetLagEarlyPrefillForTest(true)()
				defer SetAheadPreEarlyForTest(true)()
				defer SetAheadKeysCutForTest(true)()
				defer SetAheadPrepScanForTest(true)()
				lagPipeAheadClaimedForTest = waitOwnChildForTest
				defer func() { lagPipeAheadClaimedForTest = nil }()
			}
			run := func(on bool) map[string]string {
				defer SetOwnBlockPrepForTest(on)()
				o0, h0 := ownBlockPreps.Load(), aheadPrepHits.Load()
				tables := r69aRunTables(t, arrivalSealed, nil)
				preps, hits := ownBlockPreps.Load()-o0, aheadPrepHits.Load()-h0
				t.Logf("ownPrep=%v: prepared at the seal %d, runs that adopted a preparation %d", on, preps, hits)
				if on {
					require.NotZero(t, preps, "no block prepared at its seal")
					require.NotZero(t, hits, "no ahead run adopted a preparation")
				} else {
					require.Zero(t, preps)
					require.Zero(t, hits, "a sealed block was prepared with the switch off")
				}
				return tables
			}
			want := run(false)
			got := run(true)
			requireSameTables(t, want, got, "own block prep")
		})
	}
}
