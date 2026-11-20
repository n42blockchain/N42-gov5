// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// rootHintOutcome is a lane-mode block outcome with its QMDB root: the
// root, the undo record (the applied ops' old and new slots, in order) and
// whether the root took the hint.
type rootHintOutcome struct {
	laneBlockOutcome
	qmdbRoot types.Hash
	undo     *qmdb.BlockUndo
	handed   bool
	used     bool
	// hits and misses of the hinted op build.
	hits, misses int
	post         []string
}

// runRootHintBlock runs a block like runLaneBlock with the lanes on and the
// root switches given, on a state whose root computer is a fresh QMDB
// computer recording undo; unchanged applies UnchangedAccountNoOpTime.
func runRootHintBlock(t *testing.T, db kv.RwDB, cfg *params.ChainConfig, header *block.Header, txs []*transaction.Transaction,
	addrs []types.Address, fromRun, unlocked, headroom string, unchanged bool) rootHintOutcome {
	t.Helper()
	defer SetTransferLanesForTest("on")()
	defer commitment.SetRootOpsFromRunForTest(fromRun)()
	defer commitment.SetRootOpsUnlockedForTest(unlocked)()
	defer commitment.SetQMDBApplyHeadroomForTest(headroom)()
	rootHintAnyStateForTest, rootHintBuildInlineForTest = true, true
	defer func() { rootHintAnyStateForTest, rootHintBuildInlineForTest = false, false }()
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(cfg, bc, nil)
	blk := block.NewBlock(header, txs).(*block.Block)
	var out rootHintOutcome
	used0, _, _, _ := TransferLanesStats()
	hd0 := rootHintsHanded.Load()
	_, _, mm0, _, _ := commitment.RootOpsStats()
	rc := commitment.NewQMDBRootComputer()
	rc.EnableUndoRecording()
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		ibs.SetRootComputer(rc)
		run, err := sp.ExecuteParallelAhead(blk, ibs, func(uint64) types.Hash { return types.Hash{} })
		if err != nil {
			out.err = err.Error()
			return nil
		}
		out.usedGas = run.UsedGas
		for i := range run.Receipts {
			var buf bytes.Buffer
			run.Receipts.EncodeIndex(i, &buf)
			out.receipts = append(out.receipts, buf.Bytes())
		}
		out.receiptsRoot = DeriveSha(run.Receipts)
		out.logs = len(run.Logs)
		for _, a := range addrs {
			out.accounts = append(out.accounts, fmt.Sprintf("%x n=%d b=%s ch=%x e=%v", a, ibs.GetNonce(a), ibs.GetBalance(a).Hex(), ibs.GetCodeHash(a), ibs.Exist(a)))
		}
		if unchanged {
			c := *cfg
			c.UnchangedAccountNoOpTime = big.NewInt(0)
			ibs.ApplyUnchangedAccountRule(&c, header.Time)
		}
		ibs.SetRootPostStateHook(func(ps *state.PostState) {
			for _, a := range addrs {
				acc, ok := ps.AccountOf(a)
				out.post = append(out.post, fmt.Sprintf("%x %v %s", a, ok, laneAccString(acc)))
			}
		})
		out.stateRoot = ibs.IntermediateRoot()
		out.qmdbRoot = out.stateRoot
		p := commitment.LastComputeRootPhases()
		out.used = p != nil && p.HintUsed
		if out.used {
			out.hits, out.misses = p.HintHits, p.HintMisses
		}
		out.undo = rc.LastUndo()
		rec := &laneWriteRecorder{}
		require.NoError(t, ibs.CommitBlock(cfg.Rules(header.Number.Uint64()), rec))
		out.writes = rec.w
		return nil
	}))
	used1, _, _, _ := TransferLanesStats()
	out.lanesUsed = used1 > used0
	out.handed = rootHintsHanded.Load() > hd0
	_, _, mm1, _, _ := commitment.RootOpsStats()
	require.Equal(t, mm0, mm1, "root ops check found a difference")
	return out
}

// rootHintModes runs a block with the root switches off and in every other
// combination; every outcome must equal the off one. It returns the "on/on"
// outcome.
func rootHintModes(t *testing.T, db kv.RwDB, cfg *params.ChainConfig, header *block.Header, txs []*transaction.Transaction,
	addrs []types.Address, unchanged bool) (off, on rootHintOutcome) {
	t.Helper()
	off = runRootHintBlock(t, db, cfg, header, txs, addrs, "off", "off", "off", unchanged)
	require.False(t, off.handed, "a hint was handed with the switches off")
	for _, m := range [][3]string{{"on", "off", "off"}, {"off", "on", "off"}, {"off", "off", "on"}, {"on", "on", "on"},
		{"check", "check", "check"}, {"check", "on", "on"}, {"on", "check", "off"}} {
		got := runRootHintBlock(t, db, cfg, header, txs, addrs, m[0], m[1], m[2], unchanged)
		what := fmt.Sprintf("fromRun=%s unlocked=%s headroom=%s", m[0], m[1], m[2])
		requireSameLaneOutcome(t, off.laneBlockOutcome, got.laneBlockOutcome, what)
		require.Equal(t, off.qmdbRoot, got.qmdbRoot, "%s: root", what)
		require.True(t, reflect.DeepEqual(off.undo, got.undo), "%s: undo record", what)
		require.Equal(t, off.post, got.post, "%s: post-state", what)
		require.Equal(t, got.lanesUsed, got.handed, "%s: a hint is handed exactly on lane blocks", what)
		if got.lanesUsed && m[0] != "off" {
			require.True(t, got.used, "%s: the root did not take the hint", what)
		}
		if m == [3]string{"on", "on", "on"} {
			on = got
		}
	}
	return off, on
}

// TestRootOpsHintRandomBlocks: randomised plain-transfer blocks (the lanes'
// fuzz: emptied senders, recipients credited many times, the coinbase paid,
// empty and nearly-full recipients; and blocks the lanes decline) with the
// unchanged-account rule on and off give the same receipts, accounts, root,
// undo record, post-state and write set with the root switches off, on and
// checking; only lane blocks hand a hint, and their roots take it.
func TestRootOpsHintRandomBlocks(t *testing.T) {
	seeds := 24
	if testing.Short() {
		seeds = 8
	}
	used, declined := 0, 0
	for seed := int64(1); seed <= int64(seeds); seed++ {
		f := newLaneFuzz(t, 500+seed)
		var perturb []string
		if seed%3 == 0 {
			perturb = append(perturb, laneFuzzPerturbations[f.rnd.Intn(len(laneFuzzPerturbations))])
		} else if seed%3 == 1 {
			perturb = append(perturb, []string{"empty", "maxValue", "legacy", "drain"}[f.rnd.Intn(4)])
		}
		t.Run(fmt.Sprintf("seed%d_%v", seed, perturb), func(t *testing.T) {
			db := memdb.NewTestDB(t)
			specs := f.block(perturb)
			f.fund(t, db, specs)
			txs := make([]*transaction.Transaction, len(specs))
			for i, s := range specs {
				txs[i] = f.sign(t, s)
			}
			header := *f.header
			if slices.Contains(perturb, "blockGasLimit") {
				header.GasLimit = params.TxGas - 1
			}
			_, on := rootHintModes(t, db, f.cfg, &header, txs, f.addrs, seed%2 == 0)
			if on.lanesUsed {
				used++
			} else {
				declined++
			}
		})
	}
	t.Logf("lane blocks %d, declined %d", used, declined)
	require.NotZero(t, used)
	require.NotZero(t, declined)
}

// TestRootOpsHintBenchShape: bench-shaped lane blocks (sender chains,
// Zipf and modulo recipients) through every root switch combination.
func TestRootOpsHintBenchShape(t *testing.T) {
	for _, c := range []struct {
		name                 string
		txs, senders, recips int
		zipf, v              float64
	}{
		{"chains_zipf", 4000, 100, 570, 1.1, 2},
		{"chains_modulo", 3000, 75, 97, 0, 0},
		{"distinct_senders", 6000, 6000, 3000, 1.2, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newTransferBlockFixture(c.txs, c.senders, c.recips, c.zipf, c.v)
			db := memdb.NewTestDB(t)
			fundTransferBlockStateFiller(t, db, fx, 100)
			addrs := append(append(append([]types.Address{}, fx.senders...), fx.recipients...), fx.header.Coinbase)
			for _, unchanged := range []bool{false, true} {
				_, on := rootHintModes(t, db, fx.cfg, fx.header, fx.txs, addrs, unchanged)
				require.True(t, on.lanesUsed, "the bench shape did not take the lanes")
				require.True(t, on.used, "the root did not take the hint")
				// Every sender and recipient comes from the hint; the misses
				// are the fee recipients.
				t.Logf("unchanged=%v: hits %d, misses %d", unchanged, on.hits, on.misses)
				require.GreaterOrEqual(t, on.hits, c.senders)
				require.LessOrEqual(t, on.misses, 3)
			}
		})
	}
}
