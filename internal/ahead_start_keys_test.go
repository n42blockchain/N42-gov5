// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// TestAheadStartKeysSameRows (N42_AHEAD_START_KEYS_PREFETCH): every base
// prefill is held at its block-start reads until its claim and detached by
// it (N42_AHEAD_PRE_DETACH, as in TestAheadPreDetach), so each early launch
// runs without one. With the switch on the claim takes the block's
// start-keys prefill instead -- the fee recipient, the block-start keys and
// (test hook) the block's transfer recipient. With the recipient rotation 1
// every block's recipient (and the coinbase) was written by its parent, so
// the parent's post-state layer must answer it over the prefill; with 5 it
// was last written five blocks earlier, below every layer, so the prefill
// answers it and must have answered some lookups. Both chains must end with exactly the
// same rows in every table as with the switch off (and every block equal
// to the serial chain's, which the harness checks); every start-keys prefill
// goroutine ends.
func TestAheadStartKeysSameRows(t *testing.T) {
	const lockHold = 40 * time.Millisecond
	for _, mod := range []int{1, 5} {
		mod := mod
		run := func(t *testing.T, on bool) map[string]string {
			defer SetAheadStartKeysForTest(on)()
			defer SetAheadPreDetachForTest(true)()
			defer SetAheadKeysCutForTest(true)()
			defer SetLagEarlySnapshotForTest(true)()
			// As on the fleet: an early-snapshot launch keeps a prefill read
			// at or above the layer under its parent's post-state.
			defer SetLagEarlyPrefillForTest(true)()
			defer SetAheadPreEarlyForTest(true)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			defer SetAheadBaseClaimWaitForTest(true)()
			lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, mod
			defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()
			lagPipeAheadClaimedForTest = waitOwnChildForTest
			defer func() { lagPipeAheadClaimedForTest = nil }()
			aheadStartKeysExtraForTest = func(hdr *block.Header) []qmdb.Hash {
				to := types.Address{0xee, byte(hdr.Number.Uint64() % uint64(mod))}
				return []qmdb.Hash{qmdb.Hash(commitment.AccountKeyHash(to))}
			}
			defer func() { aheadStartKeysExtraForTest = nil }()
			var mu sync.Mutex
			var skEnded []*aheadBase
			var skPF []*commitment.BasePrefill
			aheadStartKeysEndForTest = func(ab *aheadBase) {
				mu.Lock()
				skEnded = append(skEnded, ab)
				skPF = append(skPF, ab.pf)
				mu.Unlock()
			}
			defer func() { aheadStartKeysEndForTest = nil }()
			// The claim finds the preparation started and, with the switch
			// on, gives the start-keys prefill a moment to finish (it is
			// never waited for by the claim itself).
			lagPipeBeforeClaimForTest = func(bc *BlockChain, child *block.Block) {
				for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Microsecond) {
					bc.aheadPreps.mu.Lock()
					pr := bc.aheadPreps.m[child.Hash()]
					bc.aheadPreps.mu.Unlock()
					if pr == nil || pr.state.Load() != prepPending {
						break
					}
				}
				if on {
					time.Sleep(5 * time.Millisecond)
				}
			}
			defer func() { lagPipeBeforeClaimForTest = nil }()
			aheadBaseSysKeysForTest = func(ab *aheadBase) {
				select {
				case <-ab.stop:
					time.Sleep(lockHold)
				case <-time.After(2 * time.Second):
				}
			}
			defer func() { aheadBaseSysKeysForTest = nil }()
			u0, n0, d0 := aheadStartKeysUsed.Load(), aheadStartKeysUnused.Load(), aheadPreDetaches.Load()
			tables := r69aRunTables(t, arrivalAny, nil)
			used, unused, detached := aheadStartKeysUsed.Load()-u0, aheadStartKeysUnused.Load()-n0, aheadPreDetaches.Load()-d0
			// Every start-keys prefill ends, used or not.
			var hits uint64
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
				mu.Lock()
				n := len(skEnded)
				mu.Unlock()
				if uint64(n) >= used+unused || time.Now().After(deadline) {
					break
				}
			}
			mu.Lock()
			for _, ab := range skEnded {
				<-ab.done
			}
			for _, pf := range skPF {
				h, _ := pf.Stats()
				hits += h
			}
			nEnded := len(skEnded)
			mu.Unlock()
			t.Logf("mod=%d start-keys=%v: detached %d, start-keys used %d, not used %d, ended %d, prefill hits %d",
				mod, on, detached, used, unused, nEnded, hits)
			require.NotZero(t, detached, "no claim detached a base prefill")
			if on {
				require.NotZero(t, used, "no claim took a start-keys prefill")
				require.EqualValues(t, used+unused, nEnded, "a start-keys prefill goroutine did not end")
				if mod > 1 {
					require.NotZero(t, hits, "the start-keys prefills answered nothing")
				}
			} else {
				require.Zero(t, used+unused+uint64(nEnded))
			}
			return tables
		}
		t.Run("mod"+string(rune('0'+mod)), func(t *testing.T) {
			want := run(t, false)
			got := run(t, true)
			requireSameTables(t, want, got, "ahead start keys")
		})
	}
}
