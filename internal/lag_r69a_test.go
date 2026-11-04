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
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// r69aRunTables runs the ordered-writer pipeline harness (every write handed
// off, slow writes, one injected write failure) with the given arrival and
// returns every table of the pipelined chain; check, when set, runs on the
// pipelined chain before its tables are read.
func r69aRunTables(t *testing.T, arrival lagArrival, check func(piped *lagChain)) map[string]string {
	t.Helper()
	var tables map[string]string
	lagPipeTablesSinkForTest = func(piped *lagChain) {
		if check != nil {
			check(piped)
		}
		tables = dumpTables(t, piped.db)
	}
	defer func() { lagPipeTablesSinkForTest = nil }()
	testLagPipelineAheadEngine(t, false, arrival, true)
	require.NotEmpty(t, tables)
	return tables
}

// r69aSlowWrites makes every ordered write sleep d first and returns the
// restore.
func r69aSlowWrites(d time.Duration) func() {
	lagWriteBeforeCommitForTest = func(uint64) error {
		time.Sleep(d)
		return nil
	}
	return func() { lagWriteBeforeCommitForTest = nil }
}

// r69aHoldFills holds every base prefill before it reads until its claim
// (or a drop) stops it, so each claim finds its fill still reading: with
// N42_AHEAD_PRE_EARLY a late child's claim then cuts it.
func r69aHoldFills() func() {
	aheadBaseBeforeFillForTest = func(ab *aheadBase) {
		select {
		case <-ab.stop:
		case <-time.After(2 * time.Second):
		}
	}
	// The claim finds the preparation started (its senders settled), as a
	// child checked well before it runs does.
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
	return func() { aheadBaseBeforeFillForTest, lagPipeBeforeClaimForTest = nil, nil }
}

// TestAheadPreEarlySameRows (r69a, N42_AHEAD_PRE_EARLY): every child is
// checked available only after its parent published its snapshot, so it is
// launched late (at its queueing), and every base prefill is still reading
// when the child claims it. With the switch off the claim waits for the
// fill; on, it stops the fill at its next key. Both chains must end with
// exactly the same rows in every table (and each the serial chain's
// results, which the harness checks), and the cut must have happened only
// with the switch on.
func TestAheadPreEarlySameRows(t *testing.T) {
	for _, c := range r68aCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.wide {
				defer r68aWideFlags()()
			}
			defer state.SetHistoryIndexDeferredForTest(c.deferred)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			defer SetAheadBaseClaimWaitForTest(true)()
			defer r69aSlowWrites(30 * time.Millisecond)()
			defer r69aHoldFills()()
			run := func(on bool) map[string]string {
				defer SetAheadPreEarlyForTest(on)()
				c0, k0 := aheadPreCutClaims.Load(), aheadPreCuts.Load()
				var enq uint64
				tables := r69aRunTables(t, arrivalMidImport, func(piped *lagChain) {
					enq = piped.bc.lagPipe.launchAtEnqueue.Load()
				})
				claims, cuts := aheadPreCutClaims.Load()-c0, aheadPreCuts.Load()-k0
				t.Logf("preEarly=%v: launched at enqueue %d, late claims with a prefill %d, fills cut %d", on, enq, claims, cuts)
				require.GreaterOrEqual(t, enq, uint64(6), "children launched late")
				if on {
					require.GreaterOrEqual(t, cuts, uint64(4), "late claims that cut a fill still reading")
				} else {
					require.Zero(t, claims)
					require.Zero(t, cuts)
				}
				return tables
			}
			want := run(false)
			got := run(true)
			requireSameTables(t, want, got, "ahead pre early")
		})
	}
}

// r69aEarlyResults records the early results published during a run.
type r69aEarlyResults struct {
	mu     sync.Mutex
	got    map[types.Hash][]rawdb.ExecutedResult
	served int
	bad    []string
}

func (e *r69aEarlyResults) hook(bc *BlockChain, hash types.Hash, r rawdb.ExecutedResult) {
	// What the gate and the build read now: the early result, the write
	// job's, or (written already) none.
	pr, ok := bc.pendingExecutedResult(hash)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.got == nil {
		e.got = make(map[types.Hash][]rawdb.ExecutedResult)
	}
	e.got[hash] = append(e.got[hash], r)
	if ok {
		e.served++
		if pr != r {
			e.bad = append(e.bad, hash.Hex()[:12])
		}
	}
}

// TestLagResultEarlySameRows (r69a, N42_LAG_RESULT_EARLY): the harness with
// slow writes, so each import's hand-off waits for the previous write, and
// one injected write failure (its block and its child execute again). With
// the switch on every lag import publishes its result before its hand-off;
// each published result must equal the result the write stored for the
// block (both of the failed block's), what the gate read at that moment
// must equal it, and the tables must be the same as with the switch off.
func TestLagResultEarlySameRows(t *testing.T) {
	for _, c := range r68aCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.wide {
				defer r68aWideFlags()()
			}
			defer state.SetHistoryIndexDeferredForTest(c.deferred)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			defer SetLagGateAppliedForTest(true)()
			defer r69aSlowWrites(30 * time.Millisecond)()
			run := func(on bool) map[string]string {
				defer SetLagResultEarlyForTest(on)()
				rec := &r69aEarlyResults{}
				lagEarlyResultForTest = rec.hook
				defer func() { lagEarlyResultForTest = nil }()
				p0, s0 := lagResultsEarly.Load(), lagResultsEarlyServed.Load()
				tables := r69aRunTables(t, arrivalBeforeSnapshot, func(piped *lagChain) {
					rec.mu.Lock()
					defer rec.mu.Unlock()
					for h, rs := range rec.got {
						want := piped.result(h)
						for _, r := range rs {
							require.Equal(t, want, r, "early result of %x differs from the stored one", h[:6])
						}
					}
				})
				published, served := lagResultsEarly.Load()-p0, lagResultsEarlyServed.Load()-s0
				rec.mu.Lock()
				defer rec.mu.Unlock()
				t.Logf("resultEarly=%v: published %d, complete %d blocks, gate reads at completion %d (from the early result %d)",
					on, published, len(rec.got), rec.served, served)
				require.Empty(t, rec.bad, "the gate read another result than the early one")
				if on {
					require.GreaterOrEqual(t, published, uint64(10), "results published before the hand-off")
					require.GreaterOrEqual(t, len(rec.got), 10)
					require.Greater(t, served, uint64(0), "no gate read was answered by an early result")
				} else {
					require.Zero(t, published)
					require.Empty(t, rec.got)
				}
				return tables
			}
			want := run(false)
			got := run(true)
			requireSameTables(t, want, got, "early result")
		})
	}
}

// TestR69aBothFlagsSameRows: both r69a switches on against both off, late
// children with their fills held.
func TestR69aBothFlagsSameRows(t *testing.T) {
	if testing.Short() {
		t.Skip("wide pipeline run")
	}
	defer r68aWideFlags()()
	defer state.SetHistoryIndexDeferredForTest(true)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	defer r69aSlowWrites(20 * time.Millisecond)()
	defer r69aHoldFills()()
	run := func(on bool) map[string]string {
		defer SetAheadPreEarlyForTest(on)()
		defer SetLagResultEarlyForTest(on)()
		return r69aRunTables(t, arrivalMidImport, nil)
	}
	want := run(false)
	got := run(true)
	requireSameTables(t, want, got, "both r69a flags")
}
