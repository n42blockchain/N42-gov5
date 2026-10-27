// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// njBlockOutcome is what an import of a block leaves, step by step: the
// state after the ahead run (exec and fold), after the Prague block end,
// after the rewards (FinalizeDigestForTest each), the requests, the
// receipts, the root and its dirty set, the post-state the root publishes
// and CommitBlock's rows.
type njBlockOutcome struct {
	afterRun, afterEnd, afterRewards string
	requests                         []hexutil.Bytes
	receipts                         [][]byte
	root                             types.Hash
	rootRows                         []string
	post                             *state.PostState
	rows                             []string
}

// njSpecial sends a few of the block's transactions to the accounts the
// Prague block end touches -- the system address (credited), the two
// request contracts (called with value) -- and to accounts absent from the
// store.
func njSpecial(i int) *types.Address {
	var a types.Address
	switch {
	case i%997 == 13:
		a = state.SystemAddress
	case i%1499 == 7:
		a = vm.WithdrawalRequestsAddress
	case i%1999 == 5:
		a = vm.ConsolidationRequestsAddress
	case i%211 == 3:
		a = types.BytesToAddress(crypto.Keccak256([]byte("nj-absent"), []byte(strconv.Itoa(i))))
	default:
		return nil
	}
	return &a
}

// fundNJContracts deploys the EIP-7002 and EIP-7251 request contracts.
func fundNJContracts(tb testing.TB, db kv.RwDB) {
	tb.Helper()
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := state.NewPlainStateWriter(tx, tx, 1)
		ibs := state.New(state.NewPlainState(tx, 1))
		for addr, code := range map[types.Address][]byte{
			vm.WithdrawalRequestsAddress:    vm.WithdrawalRequestQueueCode,
			vm.ConsolidationRequestsAddress: vm.ConsolidationRequestQueueCode,
		} {
			ibs.CreateAccount(addr, true)
			ibs.SetCode(addr, code)
			ibs.SetNonce(addr, 1)
		}
		return ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsBerlin: true, IsLondon: true}, writer)
	})
	if err != nil {
		tb.Fatalf("deploy request contracts: %v", err)
	}
}

// newNJState is the lag pipeline's state for an ahead run over tx.
func newNJState(tx kv.Tx) *state.IntraBlockState {
	prefetch := state.NewAccountPrefetch(state.LayerPostStates(nil, state.NewPlainStateReader(tx)))
	ibs := state.New(prefetch)
	ibs.SetAccountPrefetch(prefetch)
	ibs.SetDetachedPendingCredits(true)
	return ibs
}

// runNJImport runs blk as the lag pipeline imports it: the ahead run, then
// FinishParallel's block end, a reward, the root (with its post-state) and
// CommitBlock, with N42_FOLD_NOJOURNAL at on.
func runNJImport(t *testing.T, db kv.RwDB, fx *transferBlockFixture, on bool) njBlockOutcome {
	t.Helper()
	defer state.SetFoldNoJournalForTest(on)()
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(fx.cfg, bc, nil)
	blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
	tx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	ibs := newNJState(tx)
	run, err := sp.ExecuteParallelAhead(blk, ibs, func(uint64) types.Hash { return types.Hash{} })
	require.NoError(t, err)
	require.False(t, ibs.FoldNoJournal(), "the fold mode outlived the fold")
	if on {
		require.Greater(t, ibs.FoldCredits(), 100, "the no-journal fold marked few credits")
	} else {
		require.Zero(t, ibs.FoldCredits())
	}
	var out njBlockOutcome
	out.afterRun = ibs.FinalizeDigestForTest()
	for i := range run.Receipts {
		var buf bytes.Buffer
		run.Receipts.EncodeIndex(i, &buf)
		out.receipts = append(out.receipts, buf.Bytes())
	}
	out.requests, err = ProcessExecutionBlockEnd(nil, fx.cfg, ibs, fx.header, nil)
	require.NoError(t, err)
	out.afterEnd = ibs.FinalizeDigestForTest()
	ibs.AddBalance(fx.header.Coinbase, uint256.NewInt(2))
	ibs.AddBalance(fx.recipients[0], uint256.NewInt(3))
	ibs.GetBalance(fx.recipients[1])
	out.afterRewards = ibs.FinalizeDigestForTest()
	rc := &hashingRootComputer{}
	ibs.SetRootComputer(rc)
	ibs.SetRootPostStateHook(func(ps *state.PostState) { out.post = ps })
	out.root = ibs.IntermediateRoot()
	out.rootRows = append([]string(nil), rc.rows...)
	w := &recWriter{}
	require.NoError(t, ibs.CommitBlock(fx.cfg.RulesWithTimestamp(fx.header.Number.Uint64(), fx.header.Time), w))
	out.rows = w.log
	return out
}

// TestFoldNoJournalImportMatches imports a bench-shaped Prague block --
// sender chains, Zipf recipients, transactions to the system address, the
// request contracts and absent accounts -- with N42_FOLD_NOJOURNAL off and
// on and requires the same state after every step, the same requests,
// receipts, root, root dirty set, post-state and committed rows, for the
// serial and the sharded fold, the fast finalize on and off, the system
// calls finalized per call or once, N42_FINALIZE_FOLD_KEYS off and on.
func TestFoldNoJournalImportMatches(t *testing.T) {
	nTxs, nSenders, nRecips := 12000, 3000, 2500
	if testing.Short() {
		nTxs, nSenders, nRecips = 9000, 2600, 1800
	}
	fx := newTransferBlockFixtureTo(nTxs, nSenders, nRecips, 1.1, 50, njSpecial)
	rules := fx.cfg.RulesWithTimestamp(fx.header.Number.Uint64(), fx.header.Time)
	require.True(t, rules.IsPrague, "the fixture's block is not Prague")
	db := memdb.NewTestDB(t)
	fundTransferBlockStateFiller(t, db, fx, 1000)
	fundNJContracts(t, db)
	for _, shard := range []int{0, 16} {
		for _, fast := range []bool{true, false} {
			for _, once := range []bool{false, true} {
				tag := fmt.Sprintf("shard=%d fast=%v once=%v", shard, fast, once)
				t.Run(tag, func(t *testing.T) {
					prev := collectShardWorkers
					collectShardWorkers = shard
					defer func() { collectShardWorkers = prev }()
					defer state.SetFinalizeFastForTest(fast)()
					defer state.SetSyscallFinalizeOnceForTest(once)()
					want := runNJImport(t, db, fx, false)
					// N42_FINALIZE_FOLD_KEYS off and on (each use checked
					// against the maps): the same outcome.
					for _, keys := range []bool{false, true} {
						restore := state.SetFinalizeFoldKeysForTest(keys)
						defer state.SetFoldKeysVerifyForTest(keys)()
						used, _ := state.FoldKeysCounts()
						got := runNJImport(t, db, fx, true)
						restore()
						if now, _ := state.FoldKeysCounts(); keys && now == used {
							t.Fatal("the root walk did not take the fold keys")
						}
						// Before the block end only stateObjectsDirty differs: the
						// no-journal fold put its written accounts there already.
						require.Equal(t, njNoDirty(want.afterRun), njNoDirty(got.afterRun), "after the ahead run")
						require.Equal(t, want.receipts, got.receipts, "receipts")
						require.Equal(t, want.requests, got.requests, "requests")
						require.Equal(t, want.afterEnd, got.afterEnd, "after the block end")
						require.Equal(t, want.afterRewards, got.afterRewards, "after the rewards")
						require.Equal(t, want.rootRows, got.rootRows, "root dirty set")
						require.Equal(t, want.root, got.root, "root")
						require.Equal(t, want.post, got.post, "post-state")
						require.Equal(t, want.rows, got.rows, "committed rows")
					}
				})
			}
		}
	}
}

// njNoDirty drops the stateObjectsDirty membership from a finalize digest.
func njNoDirty(d string) string {
	d = strings.ReplaceAll(d, " dirty=true", "")
	return strings.ReplaceAll(d, " dirty=false", "")
}

// njBenchFixture is the fleet's block: 163,200 transfers from 8,000
// senders (chains of ~20) over 22,857 Zipf recipients
// (N42_BENCH_TXS/SENDERS/RECIPIENTS override).
var (
	njBenchOnce sync.Once
	njBenchFx   *transferBlockFixture
)

func njBenchFixture() *transferBlockFixture {
	njBenchOnce.Do(func() {
		njBenchFx = newTransferBlockFixture(benchEnvInt("N42_BENCH_TXS", 163200), benchEnvInt("N42_BENCH_SENDERS", 8000),
			benchEnvInt("N42_BENCH_RECIPIENTS", 22857), 1.1, 50)
	})
	return njBenchFx
}

// BenchmarkFoldNoJournalBlockEnd imports the fleet's block as the lag
// pipeline does (ahead run, Prague block end, reward, root through a
// capturing computer) with N42_FOLD_NOJOURNAL off and on, and reports the
// medians of the import's phases: collect and its fold (cApply), the block
// end's finalize (fSysFin), the root's preamble up to its hook (fPreHook)
// and its steps.
//
//	GOMAXPROCS=32 go test -tags nosqlite,noboltdb -run '^$' -bench BenchmarkFoldNoJournalBlockEnd -benchtime 15x ./internal/
func BenchmarkFoldNoJournalBlockEnd(b *testing.B) {
	fx := njBenchFixture()
	db := memdb.NewTestDB(b)
	fundTransferBlockStateFiller(b, db, fx, benchEnvInt("N42_BENCH_FILLER", 20000))
	fundNJContracts(b, db)
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(fx.cfg, bc, nil)
	blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	shard := benchEnvInt("N42_COLLECT_SHARD", 16)

	type sample struct{ collect, cApply, sysFin, blockEnd, preHook, pending, detach, mat, merge, sort, loop time.Duration }
	runOnce := func(b *testing.B) sample {
		tx, err := db.BeginRo(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		defer tx.Rollback()
		ibs := newNJState(tx)
		run, err := sp.ExecuteParallelAhead(blk, ibs, hashFn)
		if err != nil {
			b.Fatal(err)
		}
		t0 := time.Now()
		if _, err := ProcessExecutionBlockEnd(nil, fx.cfg, ibs, fx.header, nil); err != nil {
			b.Fatal(err)
		}
		tEnd := time.Now()
		ibs.AddBalance(fx.header.Coinbase, uint256.NewInt(2))
		ibs.SetRootComputer(&capturingNJRoot{})
		ibs.IntermediateRoot()
		rp := ibs.LastRootPrepPhases()
		ph := run.phases
		return sample{
			collect: ph.Collect, cApply: ph.CollectApply,
			sysFin:   time.Duration(lastBlockEndFinalizeNanos.Load()),
			blockEnd: tEnd.Sub(t0),
			preHook:  rp.Enter.Sub(t0) + rp.Pending + rp.Detach + rp.Mat + rp.Merge + rp.Sort + rp.Loop + rp.Tail,
			pending:  rp.Pending, detach: rp.Detach, mat: rp.Mat, merge: rp.Merge, sort: rp.Sort, loop: rp.Loop,
		}
	}
	for _, on := range []bool{false, true} {
		b.Run("nojournal="+strconv.FormatBool(on), func(b *testing.B) {
			defer state.SetFoldNoJournalForTest(on)()
			defer state.SetFinalizeFastForTest(true)()
			prev := collectShardWorkers
			collectShardWorkers = shard
			defer func() { collectShardWorkers = prev }()
			runOnce(b) // warm-up: sender memos, pools
			var ss []sample
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ss = append(ss, runOnce(b))
			}
			b.StopTimer()
			med := func(f func(sample) time.Duration) float64 {
				v := make([]time.Duration, len(ss))
				for i, s := range ss {
					v[i] = f(s)
				}
				sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
				return float64(v[len(v)/2].Microseconds()) / 1000
			}
			b.ReportMetric(med(func(s sample) time.Duration { return s.collect }), "collect-ms")
			b.ReportMetric(med(func(s sample) time.Duration { return s.cApply }), "cApply-ms")
			b.ReportMetric(med(func(s sample) time.Duration { return s.blockEnd }), "fBlockEnd-ms")
			b.ReportMetric(med(func(s sample) time.Duration { return s.sysFin }), "fSysFin-ms")
			b.ReportMetric(med(func(s sample) time.Duration { return s.preHook }), "fPreHook-ms")
			b.ReportMetric(med(func(s sample) time.Duration { return s.pending }), "fPending-ms")
			b.ReportMetric(med(func(s sample) time.Duration { return s.detach }), "fDetach-ms")
			b.ReportMetric(med(func(s sample) time.Duration { return s.merge }), "fMerge-ms")
			b.ReportMetric(med(func(s sample) time.Duration { return s.sort }), "fSort-ms")
			b.ReportMetric(med(func(s sample) time.Duration { return s.loop }), "fLoop-ms")
		})
	}
}

// capturingNJRoot is a root computer that does no work.
type capturingNJRoot struct{}

func (capturingNJRoot) ComputeRoot(map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) (types.Hash, error) {
	return types.Hash{1}, nil
}
