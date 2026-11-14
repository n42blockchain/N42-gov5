// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"math/big"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// TestScanTxsStaticMatches: the preparation's one-pass scan
// (N42_AHEAD_PREP_SCAN) answers exactly what touchesAny and
// canDeferParallelFeesStatic answer, on random lists mixing fee recipients
// as senders and recipients, creations, calldata and precompile targets,
// below and above the parallel split; a list with a sender not attached is
// reported incomplete.
func TestScanTxsStaticMatches(t *testing.T) {
	recipients := []types.Address{{0xfe, 1}, {0xfe, 2}}
	precompiles := []types.Address{{19: 1}, {19: 2}, {19: 9}}
	rng := rand.New(rand.NewSource(7))
	mk := func(i int, kind int) *transaction.Transaction {
		from := types.Address{0xaa, byte(i), byte(i >> 8)}
		to := types.Address{0xbb, byte(i), byte(i >> 8)}
		var data []byte
		toPtr := &to
		switch kind {
		case 1:
			from = recipients[rng.Intn(2)]
		case 2:
			to = recipients[rng.Intn(2)]
		case 3:
			toPtr = nil
		case 4:
			data = []byte{1}
		case 5:
			to = precompiles[rng.Intn(3)]
		}
		return transaction.NewTransaction(uint64(i), from, toPtr, uint256.NewInt(1), 21000, uint256.NewInt(1), data)
	}
	for _, n := range []int{5, 100, 3 * touchesAnyParallelMin} {
		for kind := 0; kind <= 5; kind++ {
			for _, at := range []int{0, n / 2, n - 1} {
				txs := make([]*transaction.Transaction, n)
				for i := range txs {
					txs[i] = mk(i, 0)
				}
				txs[at] = mk(at, kind)
				touches, deferOK, complete := scanTxsStatic(txs, recipients, precompiles, nil)
				require.True(t, complete)
				require.Equal(t, touchesAny(txs, recipients), touches, "n %d kind %d at %d", n, kind, at)
				require.Equal(t, canDeferParallelFeesStatic(txs, recipients, precompiles, nil), deferOK, "n %d kind %d at %d", n, kind, at)
			}
		}
		txs := make([]*transaction.Transaction, n)
		for i := range txs {
			txs[i] = mk(i, 0)
		}
		to := types.Address{0xcc}
		txs[n-1] = transaction.NewTx(&transaction.LegacyTx{To: &to, Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1)})
		_, _, complete := scanTxsStatic(txs, recipients, precompiles, nil)
		require.False(t, complete, "a list with a sender not attached was scanned as complete")
	}
	// Memo-only senders (a frame block's): the deferral check takes them,
	// touchesAny does not. Complete only while attaching them could not
	// change touchesAny's answer.
	cfg := params.MainnetQMDBChainConfig
	signer := transaction.MakeSignerWithTimestamp(cfg, big.NewInt(1), 1)
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	keyAddr := crypto.PubkeyToAddress(key.PublicKey)
	signed := func(i int, to types.Address) *transaction.Transaction {
		tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{Nonce: uint64(i), To: &to, Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1)})
		require.NoError(t, err)
		_, err = transaction.Sender(signer, tx) // the memo
		require.NoError(t, err)
		require.Nil(t, tx.From())
		return tx
	}
	for _, n := range []int{6, 2 * touchesAnyParallelMin} {
		txs := make([]*transaction.Transaction, n)
		for i := range txs {
			txs[i] = signed(i, types.Address{0xbb, byte(i)})
		}
		touches, deferOK, complete := scanTxsStatic(txs, recipients, precompiles, signer)
		require.True(t, complete)
		require.Equal(t, touchesAny(txs, recipients), touches)
		require.Equal(t, canDeferParallelFeesStatic(txs, recipients, precompiles, signer), deferOK)
		require.True(t, deferOK)
		// A memo-only sender that is a fee recipient: attaching it would
		// flip touchesAny, so the scan is not recorded.
		_, _, complete = scanTxsStatic(txs, []types.Address{keyAddr}, precompiles, signer)
		require.False(t, complete)
		// No signer to read the memos under: not recorded either.
		_, _, complete = scanTxsStatic(txs, recipients, precompiles, nil)
		require.False(t, complete)
	}
	// scanFor: only the scanned list itself, or the same objects in order.
	txs := []*transaction.Transaction{mk(0, 0), mk(1, 0), mk(2, 0), mk(3, 0), mk(4, 0), mk(5, 0)}
	pr := &aheadPrep{scan: &prepScan{txs: txs, signer: signer}}
	require.NotNil(t, pr.scanFor(txs, signer))
	require.NotNil(t, pr.scanFor(append([]*transaction.Transaction(nil), txs...), signer))
	other := append([]*transaction.Transaction(nil), txs...)
	other[3] = mk(3, 0)
	require.Nil(t, pr.scanFor(other, signer), "a different transaction object adopted the scan")
	require.Nil(t, pr.scanFor(txs[:5], signer))
	require.Nil(t, pr.scanFor(txs, nil))
	require.Nil(t, (*aheadPrep)(nil).scanFor(txs, signer))
}

// TestAheadPrepScanPipeline runs the pipeline harness (every block equals
// the serial chain's) with N42_AHEAD_PREP_SCAN on, with and without early
// snapshots: the ahead runs adopt their preparation's scans.
func TestAheadPrepScanPipeline(t *testing.T) {
	for _, es := range []bool{false, true} {
		name := "hookLaunch"
		if es {
			name = "earlySnapshot"
		}
		t.Run(name, func(t *testing.T) {
			defer SetAheadPrepScanForTest(true)()
			defer SetAheadPrepForTest(true)()
			defer SetLagEarlySnapshotForTest(es)()
			if es {
				lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
				defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()
				lagPipeAheadClaimedForTest = waitOwnChildForTest
				defer func() { lagPipeAheadClaimedForTest = nil }()
			}
			// The preparation is done when its run claims it (a child
			// checked well before it runs).
			lagPipeBeforeClaimForTest = func(bc *BlockChain, child *block.Block) {
				awaitAheadBase(t, bc, child.Hash())
			}
			defer func() { lagPipeBeforeClaimForTest = nil }()
			h0 := aheadPrepScanHits.Load()
			testLagPipelineAheadEngine(t, false, arrivalAny, true)
			hits := aheadPrepScanHits.Load() - h0
			t.Logf("scans adopted %d", hits)
			require.NotZero(t, hits, "no ahead run adopted its preparation's scans")
		})
	}
}

// waitOwnChildForTest holds an ahead run (after its claim) until its own
// child is queued, at most 2 s, so the child is there when the run's
// Block-STM ends.
func waitOwnChildForTest(bc *BlockChain, b *block.Block) {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if bc.lagQueuedChild(b.Hash()) != nil {
			return
		}
	}
}

// TestAheadKeysCut: every base prefill is held before its key collection
// until its claim (r69aHoldFills), so each early-snapshot launch's cut claim
// (N42_AHEAD_PRE_EARLY) finds it collecting. With N42_AHEAD_KEYS_CUT the
// collection stops there; off, it runs to the end and the fill stops at its
// first key. Every block equals the serial chain's.
func TestAheadKeysCut(t *testing.T) {
	for _, on := range []bool{false, true} {
		name := "off"
		if on {
			name = "on"
		}
		t.Run(name, func(t *testing.T) {
			defer SetAheadKeysCutForTest(on)()
			defer SetLagEarlySnapshotForTest(true)()
			defer SetAheadPreEarlyForTest(true)()
			defer SetLagWriteAsyncForTest(true)()
			defer SetLagWritePrepareForTest(true)()
			defer SetAheadBaseClaimWaitForTest(true)()
			defer r69aHoldFills()()
			lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
			defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()
			lagPipeAheadClaimedForTest = waitOwnChildForTest
			defer func() { lagPipeAheadClaimedForTest = nil }()
			k0, c0 := aheadKeysCuts.Load(), aheadPreCutEarlyClaims.Load()
			testLagPipelineAheadEngine(t, false, arrivalAny, true)
			cuts, claims := aheadKeysCuts.Load()-k0, aheadPreCutEarlyClaims.Load()-c0
			t.Logf("keysCut=%v: early cut claims %d, collections stopped %d", on, claims, cuts)
			require.NotZero(t, claims, "no early-snapshot launch claimed with the cut")
			if on {
				require.NotZero(t, cuts, "no claim stopped a key collection")
			} else {
				require.Zero(t, cuts)
			}
		})
	}
}

// TestEarlyPrefillUsable: a run on an early snapshot keeps a prefill read at
// or above the layer under its stack's complete bottom, never below, and
// only one that exists.
func TestEarlyPrefillUsable(t *testing.T) {
	full := state.NewEarlyPostState(&earlySnap{done: make(chan struct{})})
	one := &earlySnap{number: 10, below: full, belowNum: 9} // early(10) over complete(9)
	two := &earlySnap{number: 10, below: full, belowNum: 8} // early(10) over early(9) over complete(8)
	pf := func(at uint64) *aheadBase { return &aheadBase{pf: nil, at: at} }
	require.False(t, earlyPrefillUsable(pf(9), one), "a skipped prefill was kept")
	ok := func(at uint64, es *earlySnap) bool {
		ab := pf(at)
		ab.pf = &commitment.BasePrefill{}
		return earlyPrefillUsable(ab, es)
	}
	for at, want := range map[uint64]bool{7: false, 8: true, 9: true, 10: true, 11: false} {
		require.Equal(t, want, ok(at, one), "one layer, at %d", at)
	}
	for at, want := range map[uint64]bool{6: false, 7: true, 8: true, 9: true, 10: true} {
		require.Equal(t, want, ok(at, two), "two layers, at %d", at)
	}
	require.False(t, ok(9, &earlySnap{number: 10}), "a snapshot without its complete bottom")
}

// TestLagEarlyPrefillPipeline runs the harness with early snapshots and
// N42_LAG_EARLY_PREFILL: early launches keep their base prefill, and every
// block equals the serial chain's. With N42_LAG_EARLY_DEPTH2 too, the
// stacked runs' prefills are kept or dropped by the same rule.
func TestLagEarlyPrefillPipeline(t *testing.T) {
	for _, depth2 := range []bool{false, true} {
		name := "oneLayer"
		if depth2 {
			name = "depth2"
		}
		t.Run(name, func(t *testing.T) {
			defer SetLagEarlySnapshotForTest(true)()
			defer SetLagEarlyPrefillForTest(true)()
			defer SetLagEarlyDepth2ForTest(depth2)()
			defer SetAheadPrepForTest(true)()
			lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
			defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()
			// The prefill finishes before the run claims it (the fleet's
			// common case, run 67: p50 4 ms before the launch).
			lagPipeBeforeClaimForTest = func(bc *BlockChain, child *block.Block) {
				awaitAheadBase(t, bc, child.Hash())
			}
			defer func() { lagPipeBeforeClaimForTest = nil }()
			lagPipeAheadClaimedForTest = waitOwnChildForTest
			defer func() { lagPipeAheadClaimedForTest = nil }()
			u0 := lagEarlyPrefillUsed.Load()
			var launched uint64
			lagPipeEngineDoneForTest = func(lc *lagChain) { launched = lc.bc.lagPipe.earlyStats.launched.Load() }
			defer func() { lagPipeEngineDoneForTest = nil }()
			testLagPipelineAheadEngine(t, false, arrivalAny, true)
			used := lagEarlyPrefillUsed.Load() - u0
			t.Logf("early launches %d, kept their prefill %d", launched, used)
			require.NotZero(t, launched)
			require.NotZero(t, used, "no early launch kept its prefill")
		})
	}
}

// TestLagEarlyDepth2 forces the order run 67 parked one early launch in
// seven for: block N's run, the next run on N-1's early snapshot, ends its
// Block-STM before N-1's root hook. Each hook of N-1 waits until N's run
// stacked its snapshot on N-1's and N+1's run on it finished (or parked its
// own offer, N42_LAG_EARLY_RETRY). Every block equals the serial chain's
// (the harness), every early snapshot's store is released exactly once,
// and nothing outlives the blocks. writeAfterFold: every block credits zero
// to a sender after its fold (outside the hold set until learned): the
// stacked snapshot and its run are dropped with the unsafe one.
func TestLagEarlyDepth2(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		retry, unsafe, fleet, prefil bool
	}{{"plain", false, false, false, false}, {"retry", true, false, false, false},
		{"writeAfterFold", false, true, false, false}, {"retryWriteAfterFold", true, true, false, false},
		{"fleetFlags", true, false, true, true}, {"fleetFlagsWriteAfterFold", true, true, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			defer SetLagEarlySnapshotForTest(true)()
			defer SetLagEarlyDepth2ForTest(true)()
			defer SetLagEarlyRetryForTest(tc.retry)()
			if tc.fleet {
				defer SetLagWriteAsyncForTest(true)()
				defer SetLagWritePrepareForTest(true)()
				defer SetLagWriteEarlyPrepForTest(true)()
				defer SetLagHandoffLeanForTest(true)()
				defer SetLagWriteDeferBGForTest(true)()
				defer SetLagEarlyPublishForTest(true)()
				defer SetAheadPrepForTest(true)()
				defer SetAheadPreEarlyForTest(true)()
				defer SetAheadPrepScanForTest(true)()
				defer SetAheadKeysCutForTest(true)()
				defer parallel.SetExecLeanForTest(true)()
				defer parallel.SetWavePartitionFastForTest(true)()
				defer state.SetFinalizeFastForTest(true)()
				defer state.SetFoldNoJournalForTest(true)()
				defer state.SetPrehookCutForTest(1)()
				defer state.SetFinalizeHoistForTest(true)()
				defer state.SetFoldObjectSlabForTest(true)()
			}
			if tc.prefil {
				defer SetLagEarlyPrefillForTest(true)()
			}
			lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
			defer func() { lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0 }()

			var piped atomic.Pointer[BlockChain]
			// A run waits for its child to be queued, except a stacked one:
			// its child needs N-1 imported, which waits for it below.
			lagPipeAheadClaimedForTest = func(bc *BlockChain, b *block.Block) {
				piped.Store(bc)
				bc.lagPipe.mu.Lock()
				stacked := bc.lagPipe.next2 != nil && bc.lagPipe.next2.blk.Hash() == b.Hash()
				bc.lagPipe.mu.Unlock()
				if !stacked {
					waitOwnChildForTest(bc, b)
				}
			}
			defer func() { lagPipeAheadClaimedForTest = nil }()

			var sender types.Address
			if tc.unsafe {
				sender = crypto.PubkeyToAddress(frametest.Keys(t, 65, 83)[1].PublicKey)
				lagEarlyAfterFoldForTest = func(ibs *state.IntraBlockState, _ []*transaction.Transaction) {
					ibs.AddBalance(sender, new(uint256.Int))
				}
				defer func() { lagEarlyAfterFoldForTest = nil }()
			}

			var refMu sync.Mutex
			released := map[*earlySnap]int{}
			negative := 0
			lagEarlyUnrefForTest = func(es *earlySnap, left int32) {
				refMu.Lock()
				defer refMu.Unlock()
				switch {
				case left == 0:
					released[es]++
				case left < 0:
					negative++
				default:
					if _, ok := released[es]; !ok {
						released[es] = 0
					}
				}
			}
			defer func() { lagEarlyUnrefForTest = nil }()

			// N-1's hook waits for N's run to stack and for N+1's run on the
			// stack to finish or park.
			var forced, stackedRuns atomic.Int32
			lagPipeBeforePublishForTest = func(h types.Hash) {
				bc := piped.Load()
				if bc == nil {
					return
				}
				p := &bc.lagPipe
				for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
					p.mu.Lock()
					a, es2, a2 := p.next, p.early2, p.next2
					parked := p.parked != nil && a2 != nil && p.parkedRun == a2
					p.mu.Unlock()
					if a == nil || a.parent != h {
						return
					}
					if es2 == nil {
						select {
						case <-a.done:
							// a ended without stacking (no child yet, or
							// refused): nothing more comes before the hook.
							p.mu.Lock()
							es2 = p.early2
							p.mu.Unlock()
							if es2 == nil {
								return
							}
						default:
							continue
						}
					}
					if a2 == nil {
						continue
					}
					forced.Add(1)
					if parked {
						stackedRuns.Add(1)
						return
					}
					select {
					case <-a2.done:
						stackedRuns.Add(1)
						return
					default:
					}
				}
			}
			defer func() { lagPipeBeforePublishForTest = nil }()

			var stacked, stackedLaunch, used, parked, parkedPub, parkedDrop, unsafe uint64
			lagPipeEngineDoneForTest = func(lc *lagChain) {
				p := &lc.bc.lagPipe
				p.mu.Lock()
				require.Nil(t, p.next, "a next run outlived its blocks")
				require.Nil(t, p.next2, "a stacked run outlived its blocks")
				require.Nil(t, p.early2, "a stacked snapshot outlived its blocks")
				require.Nil(t, p.parked, "a parked snapshot outlived its blocks")
				p.mu.Unlock()
				s := &p.earlyStats
				stacked, stackedLaunch, used = s.stacked.Load(), s.stackedLaunched.Load(), s.used.Load()
				parked, parkedPub, parkedDrop, unsafe = s.parked.Load(), s.parkedPublished.Load(), s.parkedDropped.Load(), s.unsafe.Load()
			}
			defer func() { lagPipeEngineDoneForTest = nil }()
			var results int
			lagPipeResultSinkForTest = func(uint64, rawdb.ExecutedResult) { results++ }
			defer func() { lagPipeResultSinkForTest = nil }()

			testLagPipelineAheadEngine(t, false, arrivalAny, tc.fleet)
			t.Logf("forced %d (stacked run done or parked %d); stacked %d, stacked launches %d, early used %d; parked %d published %d dropped %d; unsafe %d",
				forced.Load(), stackedRuns.Load(), stacked, stackedLaunch, used, parked, parkedPub, parkedDrop, unsafe)
			require.Equal(t, 12, results)
			require.NotZero(t, stacked, "no snapshot was stacked on an early one")
			require.NotZero(t, stackedLaunch, "no child started on a stacked snapshot")
			require.NotZero(t, stackedRuns.Load(), "no stacked run finished before its grandparent's hook")
			require.Equal(t, parked, parkedPub+parkedDrop, "a parked snapshot was neither published nor dropped")
			if tc.unsafe {
				require.NotZero(t, unsafe, "the write after the fold was not detected")
			} else {
				require.Zero(t, unsafe)
				require.GreaterOrEqual(t, used, uint64(6), "children started early were not used")
			}
			require.Eventually(t, func() bool {
				refMu.Lock()
				defer refMu.Unlock()
				for _, n := range released {
					if n != 1 {
						return false
					}
				}
				return true
			}, 10*time.Second, time.Millisecond, "an early snapshot's store was not released exactly once")
			refMu.Lock()
			require.Zero(t, negative, "an early snapshot dropped more references than it held")
			require.NotEmpty(t, released)
			refMu.Unlock()
		})
	}
}

// TestLagEarlyDepth2ReleasesOnce drives a stacked snapshot through each of
// its outcomes on a bare pipeline: the grandparent's hook rotating the
// stack (and a parked third offer stacked there), an unsafe grandparent, a
// reset, the stacking run superseded. Every store is released exactly once,
// after its slot and every run reading it let it go, and a stacked run
// holds a reference on both snapshots it reads.
func TestLagEarlyDepth2ReleasesOnce(t *testing.T) {
	defer SetLagEarlySnapshotForTest(true)()
	defer SetLagEarlyDepth2ForTest(true)()
	g0, h1 := types.Hash{0x10}, types.Hash{0x11}
	type fx struct {
		bc     *BlockChain
		gp     *earlySnap // early(1) over complete(0)
		a, a2  *aheadExec // run of 2 on early(1); run of 3 on early(2) over early(1)
		es2    *earlySnap // early(2), stacked
		freed  map[string]*int
		bottom *state.PostState
	}
	hdrOf := func(n uint64, parent types.Hash) *block.Header {
		return &block.Header{Number: uint256.NewInt(n), ParentHash: parent}
	}
	blkOf := func(n uint64, parent types.Hash) *block.Block {
		return block.NewBlock(hdrOf(n, parent), nil).(*block.Block)
	}
	setup := func(t *testing.T) *fx {
		f := &fx{bc: &BlockChain{}, freed: map[string]*int{"gp": new(int), "es2": new(int)}}
		p := &f.bc.lagPipe
		f.bottom = state.NewEarlyPostState(&earlySnap{done: make(chan struct{})})
		f.gp = &earlySnap{hash: h1, number: 1, header: hdrOf(1, g0), below: f.bottom, belowNum: 0, belowParent: types.Hash{},
			done: make(chan struct{}), free: func() { *f.freed["gp"]++ }}
		f.gp.post = state.NewEarlyPostState(f.gp)
		f.gp.refs.Store(1) // the publisher
		p.early = f.gp
		f.a = &aheadExec{blk: blkOf(2, h1), parent: h1, done: make(chan struct{}), pipe: p, es: f.gp, parPost: f.gp.post, parHeader: f.gp.header}
		f.gp.refs.Add(1) // a reads it
		p.next = f.a
		require.True(t, f.bc.lagPipeEarly(f.a, nil, 0, nil, func() { *f.freed["es2"]++ }), "the offer was not stacked")
		p.mu.Lock()
		f.es2 = p.early2
		p.mu.Unlock()
		require.NotNil(t, f.es2)
		require.Same(t, f.es2, f.a.pub)
		require.Same(t, f.gp, f.es2.parentEs)
		require.Equal(t, []*state.PostState{f.gp.post}, f.es2.mid, "the stacked snapshot does not sit on its parent's early layer")
		require.Same(t, f.bottom, f.es2.below)
		require.Equal(t, uint64(1), p.earlyStats.stacked.Load())
		// The run of 3 on the stack, as lagPipeLaunchEarly starts it.
		h2 := f.a.blk.Hash()
		f.a2 = &aheadExec{blk: blkOf(3, h2), parent: h2, done: make(chan struct{}), pipe: p, es: f.es2, es2: f.gp, esCheck: f.gp}
		f.es2.refs.Add(1)
		f.gp.refs.Add(1)
		p.mu.Lock()
		p.next2 = f.a2
		p.mu.Unlock()
		return f
	}
	// A run's goroutine ending (runAhead's deferred unrefs).
	runEnds := func(a *aheadExec) {
		close(a.done)
		if a.es != nil {
			a.es.unref()
		}
		if a.es2 != nil {
			a.es2.unref()
		}
		if a.pub != nil {
			a.pub.unref()
		}
	}
	full := func() *state.PostState { return state.NewEarlyPostState(&earlySnap{done: make(chan struct{})}) }
	once := func(t *testing.T, f *fx, names ...string) {
		t.Helper()
		for _, n := range names {
			require.Eventually(t, func() bool { return *f.freed[n] == 1 }, 2*time.Second, time.Millisecond, "%s not released", n)
		}
		time.Sleep(10 * time.Millisecond)
		for n, c := range f.freed {
			require.LessOrEqual(t, *c, 1, "%s released twice", n)
		}
	}

	t.Run("rotatedAtGrandparentHook", func(t *testing.T) {
		f := setup(t)
		p := &f.bc.lagPipe
		full1 := full()
		p.mu.Lock()
		require.True(t, p.earlyPublishLocked(h1, full1, map[types.Address]struct{}{}))
		require.Same(t, f.a, p.spec, "the next run was not promoted")
		require.Same(t, f.es2, p.early, "the stacked snapshot did not move down")
		require.Same(t, f.a2, p.next, "the stacked run did not move down")
		require.Nil(t, p.early2)
		require.Nil(t, p.next2)
		p.mu.Unlock()
		require.True(t, f.gp.ok.Load())
		// The stack under early(2) now collapses to early(1)'s complete
		// post-state for a snapshot stacked on it.
		mid, below, bp, bn, ok := f.es2.stackBelow()
		require.True(t, ok)
		require.Equal(t, []*state.PostState{f.es2.post}, mid)
		require.Same(t, full1, below)
		require.Equal(t, g0, bp)
		require.Equal(t, uint64(1), bn)
		runEnds(f.a)
		require.Zero(t, *f.freed["gp"], "released while the stacked run reads it")
		runEnds(f.a2)
		once(t, f, "gp")
		require.Zero(t, *f.freed["es2"], "released while still published")
		p.mu.Lock()
		p.spec = nil // 2's import took its run
		require.False(t, p.earlyPublishLocked(f.a.blk.Hash(), full(), map[types.Address]struct{}{}))
		p.mu.Unlock()
		once(t, f, "gp", "es2")
	})
	t.Run("parkedThirdStackedAtRotation", func(t *testing.T) {
		defer SetLagEarlyRetryForTest(true)()
		f := setup(t)
		p := &f.bc.lagPipe
		f.freed["es3"] = new(int)
		// 3's run ends its Block-STM before 1's hook: a third layer, parked.
		require.True(t, f.bc.lagPipeEarly(f.a2, nil, 0, nil, func() { *f.freed["es3"]++ }))
		p.mu.Lock()
		es3 := p.parked
		require.NotNil(t, es3)
		require.Same(t, f.a2, p.parkedRun)
		require.Nil(t, es3.post, "a parked offer was published")
		full1 := full()
		require.True(t, p.earlyPublishLocked(h1, full1, map[types.Address]struct{}{}))
		require.Nil(t, p.parked)
		require.Same(t, es3, p.early2, "the parked offer was not stacked at the rotation")
		require.Same(t, f.es2, es3.parentEs)
		require.Equal(t, []*state.PostState{f.es2.post}, es3.mid)
		require.Same(t, full1, es3.below, "the third offer does not sit on the grandparent's complete post-state")
		require.Equal(t, uint64(1), es3.belowNum)
		require.Equal(t, g0, es3.belowParent)
		p.mu.Unlock()
		require.Equal(t, uint64(1), p.earlyStats.parkedPublished.Load())
		runEnds(f.a)
		runEnds(f.a2)
		once(t, f, "gp")
		p.mu.Lock()
		p.spec = nil // 2's import took its run
		require.True(t, p.earlyPublishLocked(f.a.blk.Hash(), full(), map[types.Address]struct{}{}))
		require.Nil(t, p.early2, "the stacked snapshot outlived its parent's rotation")
		require.Same(t, es3, p.early)
		require.Same(t, f.a2, p.spec)
		p.spec = nil // 3's import took its run
		require.False(t, p.earlyPublishLocked(f.a2.blk.Hash(), full(), map[types.Address]struct{}{}))
		p.mu.Unlock()
		once(t, f, "gp", "es2", "es3")
	})
	t.Run("droppedUnsafeGrandparent", func(t *testing.T) {
		f := setup(t)
		p := &f.bc.lagPipe
		p.mu.Lock()
		require.False(t, p.earlyPublishLocked(h1, full(), map[types.Address]struct{}{{0x99}: {}}))
		require.Same(t, f.a, p.spec)
		require.Nil(t, p.early)
		require.Nil(t, p.early2, "the stacked snapshot survived its unsafe parent")
		require.Nil(t, p.next2, "the stacked run survived its unsafe grandparent")
		p.mu.Unlock()
		require.True(t, f.es2.dead.Load())
		require.False(t, f.gp.ok.Load())
		runEnds(f.a)
		runEnds(f.a2)
		once(t, f, "gp", "es2")
	})
	t.Run("droppedByReset", func(t *testing.T) {
		f := setup(t)
		runEnds(f.a2)
		f.bc.lagPipeReset()
		p := &f.bc.lagPipe
		require.Nil(t, p.early)
		require.Nil(t, p.early2)
		require.Nil(t, p.next)
		require.Nil(t, p.next2)
		runEnds(f.a)
		once(t, f, "gp", "es2")
	})
	t.Run("stackingRunSuperseded", func(t *testing.T) {
		f := setup(t)
		p := &f.bc.lagPipe
		other := &aheadExec{blk: f.a.blk, parent: h1, done: make(chan struct{}), pipe: p}
		close(other.done)
		p.mu.Lock()
		p.spec = other
		require.False(t, p.earlyPublishLocked(h1, full(), map[types.Address]struct{}{}))
		require.Nil(t, p.early2, "the stacked snapshot survived its run's discard")
		require.Nil(t, p.next2)
		p.mu.Unlock()
		runEnds(f.a)
		runEnds(f.a2)
		once(t, f, "gp", "es2")
	})
	t.Run("forgetStackedRunsBlock", func(t *testing.T) {
		f := setup(t)
		// 3 forgotten: the run on the stack goes, its stacked snapshot with
		// it; early(1) and 2's run stay.
		f.bc.lagPipeForget(f.a2.blk.Hash())
		p := &f.bc.lagPipe
		p.mu.Lock()
		require.Nil(t, p.early2)
		require.Nil(t, p.next2)
		require.Same(t, f.gp, p.early, "forgetting the stacked run's block dropped early(1)")
		require.Same(t, f.a, p.next)
		p.mu.Unlock()
		runEnds(f.a2)
		runEnds(f.a)
		once(t, f, "es2")
		require.Zero(t, *f.freed["gp"], "early(1) released while published")
		f.bc.lagPipeReset()
		once(t, f, "gp", "es2")
	})
	t.Run("forgetStackingBlock", func(t *testing.T) {
		f := setup(t)
		// 2 forgotten: its run, its stacked snapshot and the run on it go.
		f.bc.lagPipeForget(f.a.blk.Hash())
		p := &f.bc.lagPipe
		p.mu.Lock()
		require.Nil(t, p.early2)
		require.Nil(t, p.next2)
		require.Nil(t, p.next)
		p.mu.Unlock()
		runEnds(f.a2)
		runEnds(f.a)
		once(t, f, "gp", "es2")
	})
}

// TestLagEarlySnapshotsDoNotChainDepth2 is TestLagEarlySnapshotsDoNotChain
// with N42_LAG_EARLY_DEPTH2 (and the retry): every block's hook waits until
// its child's run stacked, so blocks keep starting two early layers deep.
// Only the last few snapshots may stay reachable, and every store is
// released.
func TestLagEarlySnapshotsDoNotChainDepth2(t *testing.T) {
	const blocks = 30
	defer SetLagEarlySnapshotForTest(true)()
	defer SetLagEarlyRetryForTest(true)()
	defer SetLagEarlyDepth2ForTest(true)()
	lagPipeBlocksForTest = blocks
	lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 3, true, 2
	defer func() {
		lagPipeBlocksForTest = 0
		lagPipeDeferDepthForTest, lagPipeRotateToForTest, lagPipeRotateModForTest = 0, false, 0
	}()
	var piped atomic.Pointer[BlockChain]
	lagPipeAheadClaimedForTest = func(bc *BlockChain, b *block.Block) {
		piped.Store(bc)
		bc.lagPipe.mu.Lock()
		stacked := bc.lagPipe.next2 != nil && bc.lagPipe.next2.blk.Hash() == b.Hash()
		bc.lagPipe.mu.Unlock()
		if !stacked {
			waitOwnChildForTest(bc, b)
		}
	}
	defer func() { lagPipeAheadClaimedForTest = nil }()
	var stackedAtHook atomic.Int32
	lagPipeBeforePublishForTest = func(h types.Hash) {
		bc := piped.Load()
		if bc == nil {
			return
		}
		p := &bc.lagPipe
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			p.mu.Lock()
			a, es2 := p.next, p.early2
			p.mu.Unlock()
			if a == nil || a.parent != h {
				return
			}
			if es2 != nil {
				stackedAtHook.Add(1)
				return
			}
			select {
			case <-a.done:
				return
			default:
			}
		}
	}
	defer func() { lagPipeBeforePublishForTest = nil }()

	var mu sync.Mutex
	seen := map[weak.Pointer[earlySnap]]bool{}
	var freed int
	lagEarlyUnrefForTest = func(es *earlySnap, left int32) {
		mu.Lock()
		defer mu.Unlock()
		seen[weak.Make(es)] = true
		if left == 0 {
			freed++
		}
	}
	defer func() { lagEarlyUnrefForTest = nil }()
	var hooks, live, total int
	lagPipeAfterPublishForTest = func(types.Hash) {
		if hooks++; hooks != blocks-3 {
			return
		}
		runtime.GC()
		runtime.GC()
		mu.Lock()
		defer mu.Unlock()
		total = len(seen)
		for w := range seen {
			if w.Value() != nil {
				live++
			}
		}
	}
	defer func() { lagPipeAfterPublishForTest = nil }()

	testLagPipelineAheadEngine(t, false, arrivalAny, false)
	mu.Lock()
	defer mu.Unlock()
	t.Logf("stacked at a hook %d; snapshots seen %d at the check, reachable %d; seen in all %d, freed %d",
		stackedAtHook.Load(), total, live, len(seen), freed)
	require.NotZero(t, stackedAtHook.Load(), "no snapshot was stacked before its grandparent's hook")
	require.GreaterOrEqual(t, total, blocks/2, "too few early snapshots to check a chain")
	require.LessOrEqual(t, live, 8, "finished early snapshots stay reachable: a chain of stores")
	require.GreaterOrEqual(t, freed, len(seen)-3, "early snapshots whose store was never released")
}
