// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/sha256"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// lagPipeChainSinkForTest, when set, receives the pipelined chain once the
// harness compared its results with the serial chain's.
var lagPipeChainSinkForTest func(lc *lagChain)

// lagChainDigestTables are the tables lagChainDigest hashes: the account
// and storage state, the change sets, the QMDB tree's persisted entries and
// index, the receipts and logs, the executed results and the headers. Not
// kv.PlainState: on this store it holds u64-keyed id lists whose order
// follows the write timing (two flag-off runs differ there).
var lagChainDigestTables = []string{
	"Account", "Storage", kv.Code, kv.AccountChangeSet, kv.StorageChangeSet,
	modules.QMDBEntries, modules.QMDBIndex, modules.QMDBTwigs, modules.QMDBTwigLeaves, modules.QMDBKeyVers,
	kv.Receipts, kv.Log, kv.ExecutedResult, kv.HeaderCanonical, kv.Headers,
}

// lagChainDigest hashes every row of the digest tables of lc's store, per
// table (empty tables left out).
func lagChainDigest(t *testing.T, lc *lagChain) map[string][32]byte {
	t.Helper()
	out := make(map[string][32]byte)
	require.NoError(t, lc.db.View(context.Background(), func(tx kv.Tx) error {
		for _, table := range lagChainDigestTables {
			h := sha256.New()
			rows := 0
			err := tx.ForEach(table, nil, func(k, v []byte) error {
				var n [8]byte
				n[0], n[1] = byte(len(k)), byte(len(v))
				h.Write(n[:2])
				h.Write(k)
				h.Write(v)
				rows++
				return nil
			})
			if err != nil || rows == 0 {
				continue
			}
			var d [32]byte
			copy(d[:], h.Sum(nil))
			out[table] = d
		}
		return nil
	}))
	return out
}

// nowaitOutcomes collects the late prefills' landing decisions.
type nowaitOutcomes struct {
	mu                 sync.Mutex
	landed, landedDeep int
	skips              map[string]int
}

func (o *nowaitOutcomes) record(l *aheadLate) {
	landed, _, depth, skip := l.stats()
	o.mu.Lock()
	defer o.mu.Unlock()
	if landed {
		o.landed++
		if depth >= 2 {
			o.landedDeep++
		}
		return
	}
	if o.skips == nil {
		o.skips = make(map[string]int)
	}
	o.skips[skip]++
}

// TestLagPipelineAheadPrefillNoWait (N42_AHEAD_PREFILL_NOWAIT): the
// pipeline harness -- children queued before their parents' snapshots,
// every sender sending in every block and each recipient last written two
// blocks earlier -- with the no-wait claim off and on. Every block's
// executed result (state root, receipt root, bloom, gas) must equal the
// serial chain's (the harness checks) and the flag-off run's, and so must
// every committed row of the pipelined chain's store. The cases force the
// claim to find the fill unfinished:
//
//   - slowFill: every fill starts 40 ms late, so the runs start on an empty
//     slot and the prefills land mid-run;
//   - slowRoot: every ComputeRoot holds the tree's lock for 30 ms after its
//     label, so the fills wait behind the parent's tree apply;
//   - runEnds: the landing waits until the run released its readers: every
//     prefill falls back (the run read per lookup throughout);
//   - orderedWriter: HotStuff's ordered writer with slow writes (the
//     grandparent's pending write layered);
//   - deep / deepMissingLayer: N42_AHEAD_BASE_DEPTH=2 at deferred depth 3
//     with recipients credited by the grandparent: prefills read below the
//     parent land over the kept post-states, or fall back when they are
//     gone.
func TestLagPipelineAheadPrefillNoWait(t *testing.T) {
	lagPipeAwaitPrepForTest = true
	defer func() { lagPipeAwaitPrepForTest = false }()
	prevEth, prevBlake := block.UseEthereumTxRoot, block.TxRootBlake3Time
	defer func() { block.UseEthereumTxRoot, block.TxRootBlake3Time = prevEth, prevBlake }()
	defer SetAheadPrepForTest(true)()
	defer SetAheadBasePrefetchForTest(true)()
	defer SetAheadBaseClaimWaitForTest(true)()
	lagPipeRotateToForTest = true
	defer func() { lagPipeRotateToForTest = false }()

	type out struct {
		results                  map[uint64]rawdb.ExecutedResult
		rows                     map[string][32]byte
		claims, landed, fallback uint64
		o                        *nowaitOutcomes
	}
	ordered := false
	run := func(t *testing.T, on bool) out {
		defer SetAheadPrefillNoWaitForTest(on)()
		o := &nowaitOutcomes{}
		aheadLateEndedForTest = o.record
		defer func() { aheadLateEndedForTest = nil }()
		res := out{results: make(map[uint64]rawdb.ExecutedResult), o: o}
		lagPipeResultSinkForTest = func(n uint64, r rawdb.ExecutedResult) { res.results[n] = r }
		defer func() { lagPipeResultSinkForTest = nil }()
		lagPipeChainSinkForTest = func(lc *lagChain) { res.rows = lagChainDigest(t, lc) }
		defer func() { lagPipeChainSinkForTest = nil }()
		c0, l0, f0 := AheadPrefillNoWaitStats()
		testLagPipelineAheadEngine(t, false, arrivalBeforeSnapshot, ordered)
		// Late prefills of the last blocks may still be landing.
		require.Eventually(t, func() bool {
			c, l, f := AheadPrefillNoWaitStats()
			return c-c0 == (l-l0)+(f-f0)
		}, 10*time.Second, time.Millisecond, "late prefills never decided")
		c1, l1, f1 := AheadPrefillNoWaitStats()
		res.claims, res.landed, res.fallback = c1-c0, l1-l0, f1-f0
		o.mu.Lock()
		t.Logf("no-wait claims %d, landed %d (deep %d), fallbacks %d %v", res.claims, res.landed, o.landedDeep, res.fallback, o.skips)
		o.mu.Unlock()
		require.NotEmpty(t, res.results)
		require.NotEmpty(t, res.rows)
		return res
	}
	same := func(t *testing.T, ref, got out) {
		t.Helper()
		require.Equal(t, len(ref.results), len(got.results))
		for n, r := range ref.results {
			require.Equal(t, r, got.results[n], "block %d: result differs with the no-wait claim", n)
		}
		var diff []string
		for table, d := range ref.rows {
			if got.rows[table] != d {
				diff = append(diff, table)
			}
		}
		for table := range got.rows {
			if _, ok := ref.rows[table]; !ok {
				diff = append(diff, table)
			}
		}
		sort.Strings(diff)
		require.Empty(t, diff, "committed rows differ with the no-wait claim")
		require.GreaterOrEqual(t, len(ref.rows), 8, "the digest found few tables")
	}
	slowFill := func(d time.Duration) func() {
		fn := func(uint64) { time.Sleep(d) }
		aheadBaseBeforeFillForTest.Store(&fn)
		return func() { aheadBaseBeforeFillForTest.Store(nil) }
	}

	var ref out
	t.Run("off", func(t *testing.T) {
		ref = run(t, false)
	})
	t.Run("offAgain", func(t *testing.T) {
		// The digest is deterministic across runs.
		same(t, ref, run(t, false))
	})
	t.Run("on", func(t *testing.T) {
		same(t, ref, run(t, true))
	})
	t.Run("slowFill", func(t *testing.T) {
		defer slowFill(40 * time.Millisecond)()
		off := run(t, false)
		same(t, ref, off)
		require.Zero(t, off.claims)
		// Even blocks read only once their late prefill landed (every
		// lookup goes through the slot); odd ones start on the empty slot
		// (their prefill lands mid-run or after the run).
		lagPipeAheadClaimedForTest = func(bc *BlockChain, child *block.Block) {
			if child.Number64().Uint64()%2 != 0 {
				return
			}
			bc.lagPipe.mu.Lock()
			var late *aheadLate
			if a := bc.lagPipe.spec; a != nil && a.blk == child {
				late = a.late
			}
			bc.lagPipe.mu.Unlock()
			if late == nil {
				return
			}
			select {
			case <-late.ended:
			case <-time.After(5 * time.Second):
				t.Errorf("the late prefill of %d never landed", child.Number64().Uint64())
			}
		}
		defer func() { lagPipeAheadClaimedForTest = nil }()
		on := run(t, true)
		same(t, ref, on)
		require.GreaterOrEqual(t, on.claims, uint64(8), "the claims waited for the slow fills")
		require.GreaterOrEqual(t, on.landed, uint64(4), "the late prefills did not land")
	})
	t.Run("slowRoot", func(t *testing.T) {
		defer commitment.SetComputeRootHeldForTest(func() { time.Sleep(30 * time.Millisecond) })()
		defer slowFill(5 * time.Millisecond)()
		same(t, ref, run(t, false))
		on := run(t, true)
		same(t, ref, on)
		require.GreaterOrEqual(t, on.claims, uint64(8), "the claims waited for the fills behind the parent's root")
		require.NotZero(t, on.landed)
	})
	t.Run("runEnds", func(t *testing.T) {
		defer slowFill(20 * time.Millisecond)()
		released := make(chan struct{}, 64)
		lagPipeAheadReleasedForTest.Store(&released)
		aheadPrefillLandForTest = func() {
			// Land only once a run released its readers after the claim.
			select {
			case <-released:
			case <-time.After(5 * time.Second):
			}
			time.Sleep(time.Millisecond)
		}
		defer func() { aheadPrefillLandForTest = nil; lagPipeAheadReleasedForTest.Store(nil) }()
		on := run(t, true)
		same(t, ref, on)
		require.GreaterOrEqual(t, on.claims, uint64(8))
		require.NotZero(t, on.o.skips["run ended before the fill"], "no run ended before its prefill landed")
	})

	t.Run("orderedWriter", func(t *testing.T) {
		// HotStuff's ordered writer, each write slow: every child's run
		// layers its grandparent's pending write, and the late prefills
		// land over it.
		defer SetLagWriteAsyncForTest(true)()
		lagWriteBeforeCommitForTest = func(uint64) error {
			time.Sleep(30 * time.Millisecond)
			return nil
		}
		defer func() { lagWriteBeforeCommitForTest = nil }()
		ordered = true
		defer func() { ordered = false }()
		defer slowFill(10 * time.Millisecond)()
		off := run(t, false)
		on := run(t, true)
		same(t, off, on)
		require.GreaterOrEqual(t, on.claims, uint64(8))
		require.NotZero(t, on.landed)
	})

	// The deep cases run a different chain (deferred depth 3, recipients
	// rotating mod 2, so each block credits the account its grandparent
	// credited): their own flag-off reference. Each grandchild's prefill
	// takes the tree before its grandparent's root -- two blocks below its
	// parent -- and then holds its fill until its run's claim, so the claim
	// finds it unfinished.
	deep := func(t *testing.T, on bool) out {
		defer SetAheadBaseDepthForTest(2)()
		lagPipeDeferDepthForTest, lagPipeRotateModForTest, lagPipeAwaitGrandBaseForTest = 3, 2, true
		defer func() { lagPipeDeferDepthForTest, lagPipeRotateModForTest, lagPipeAwaitGrandBaseForTest = 0, 0, false }()
		var mu sync.Mutex
		started, claimed := make(map[uint64]chan struct{}), make(map[uint64]chan struct{})
		gate := func(m map[uint64]chan struct{}, n uint64) chan struct{} {
			mu.Lock()
			defer mu.Unlock()
			if m[n] == nil {
				m[n] = make(chan struct{})
			}
			return m[n]
		}
		var closeMu sync.Mutex
		closeOnce := func(ch chan struct{}) {
			closeMu.Lock()
			defer closeMu.Unlock()
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
		fill := func(n uint64) {
			closeOnce(gate(started, n))
			if !on {
				// The claim waits for the fill: nothing to hold.
				return
			}
			select {
			case <-gate(claimed, n):
			case <-time.After(2 * time.Second):
			}
		}
		aheadBaseBeforeFillForTest.Store(&fill)
		defer aheadBaseBeforeFillForTest.Store(nil)
		lagPipeAwaitGrandFillForTest = func(grand *block.Block) {
			select {
			case <-gate(started, grand.Number64().Uint64()):
			case <-time.After(2 * time.Second):
			}
		}
		defer func() { lagPipeAwaitGrandFillForTest = nil }()
		lagPipeAheadClaimedForTest = func(_ *BlockChain, child *block.Block) {
			closeOnce(gate(claimed, child.Number64().Uint64()))
		}
		defer func() { lagPipeAheadClaimedForTest = nil }()
		return run(t, on)
	}
	var deepRef out
	t.Run("deepOff", func(t *testing.T) {
		deepRef = deep(t, false)
	})
	t.Run("deep", func(t *testing.T) {
		on := deep(t, true)
		same(t, deepRef, on)
		require.GreaterOrEqual(t, on.claims, uint64(8), "the claims waited for the held fills")
		require.NotZero(t, on.o.landedDeep, "no prefill read below the parent landed")
	})
	t.Run("deepMissingLayer", func(t *testing.T) {
		// The grandparent's kept post-state is gone when the claim looks
		// for it: the run keeps the parent's layers only, and a prefill
		// read below them falls back to per-lookup reads.
		lagPipeAheadLayersForTest = func(p *lagPipeline) {
			p.mu.Lock()
			p.older = nil
			p.mu.Unlock()
		}
		defer func() { lagPipeAheadLayersForTest = nil }()
		on := deep(t, true)
		same(t, deepRef, on)
		require.GreaterOrEqual(t, on.claims, uint64(8))
		require.Zero(t, on.o.landedDeep, "a prefill read below the parent landed without its layers")
		require.NotZero(t, on.o.skips["an ancestor's post-state is not kept"], "no prefill fell back for a missing layer")
	})
}
