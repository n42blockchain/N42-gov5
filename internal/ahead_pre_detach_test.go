// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
)

// TestAheadPreDetach (N42_AHEAD_PRE_DETACH): every base prefill reaches its
// block-start reads and is held there until its claim stops it, then for
// another 80 ms (the tree's reader lock behind a root, run 72), so each
// early-snapshot launch's cut claim finds it collecting its keys. Off, the
// claim waits for the collection; on, it detaches the prefill and waits only
// for the transaction readers. Both chains must end with the same rows in
// every table (and each block equal to the serial chain's, which the harness
// checks); every prefill's key lists are released exactly once, a detached
// prefill builds nothing, and every prefill goroutine ends.
func TestAheadPreDetach(t *testing.T) {
	const lockHold = 80 * time.Millisecond
	run := func(t *testing.T, on bool) map[string]string {
		defer SetAheadPreDetachForTest(on)()
		defer SetAheadKeysCutForTest(true)()
		defer SetLagEarlySnapshotForTest(true)()
		defer SetAheadPreEarlyForTest(true)()
		defer SetLagWriteAsyncForTest(true)()
		defer SetLagWritePrepareForTest(true)()
		defer SetAheadBaseClaimWaitForTest(true)()
		lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
		defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()
		lagPipeAheadClaimedForTest = waitOwnChildForTest
		defer func() { lagPipeAheadClaimedForTest = nil }()
		// The claim finds the preparation started (r69aHoldFills' rule).
		lagPipeBeforeClaimForTest = func(bc *BlockChain, child *block.Block) {
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Microsecond) {
				bc.aheadPreps.mu.Lock()
				pr := bc.aheadPreps.m[child.Hash()]
				bc.aheadPreps.mu.Unlock()
				if pr == nil || pr.state.Load() != prepPending {
					return
				}
			}
		}
		defer func() { lagPipeBeforeClaimForTest = nil }()
		var mu sync.Mutex
		var started, ended []*aheadBase
		aheadBaseSysKeysForTest = func(ab *aheadBase) {
			mu.Lock()
			started = append(started, ab)
			mu.Unlock()
			select {
			case <-ab.stop:
				time.Sleep(lockHold)
			case <-time.After(2 * time.Second):
			}
		}
		aheadBaseEndForTest = func(ab *aheadBase) {
			mu.Lock()
			ended = append(ended, ab)
			mu.Unlock()
		}
		defer func() { aheadBaseSysKeysForTest, aheadBaseEndForTest = nil, nil }()
		d0, c0 := aheadPreDetaches.Load(), aheadPreCutEarlyClaims.Load()
		tables := r69aRunTables(t, arrivalAny, nil)
		detached, claims := aheadPreDetaches.Load()-d0, aheadPreCutEarlyClaims.Load()-c0
		// Every prefill that reached its block-start reads ends.
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			mu.Lock()
			n, m := len(started), len(ended)
			mu.Unlock()
			if m >= n || time.Now().After(deadline) {
				break
			}
		}
		mu.Lock()
		defer mu.Unlock()
		endedSet := map[*aheadBase]bool{}
		for _, ab := range ended {
			endedSet[ab] = true
		}
		nDetached, slow := 0, 0
		for _, ab := range started {
			require.True(t, endedSet[ab], "a prefill goroutine did not end")
			<-ab.done
			require.EqualValues(t, 1, ab.keysReleased.Load(), "key lists released once")
			if ab.phase.Load() == abPhaseDetached {
				nDetached++
				require.Nil(t, ab.pf, "a detached prefill built a prefill")
				if time.Duration(ab.detachWait.Load()) >= lockHold/2 {
					slow++
				}
			}
		}
		t.Logf("detach=%v: early cut claims %d, prefills at their block-start reads %d, detached %d (%d of them waited)", on, claims, len(started), detached, slow)
		require.NotZero(t, claims, "no early-snapshot launch claimed with the cut")
		require.NotEmpty(t, started)
		if on {
			require.NotZero(t, detached, "no claim detached a prefill")
			require.EqualValues(t, detached, nDetached)
			require.Zero(t, slow, "a detaching claim waited for the block-start reads")
		} else {
			require.Zero(t, detached)
			require.Zero(t, nDetached)
		}
		return tables
	}
	want := run(t, false)
	got := run(t, true)
	requireSameTables(t, want, got, "ahead pre detach")
}
