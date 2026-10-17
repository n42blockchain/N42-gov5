// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// fast2AheadOutcome is what an ahead run (ExecuteParallelAhead) produced: the state root, every receipt's encoding, the
// receipts root and the gas.
type fast2AheadOutcome struct {
	stateRoot    types.Hash
	receiptsRoot types.Hash
	receiptBytes [][]byte
	usedGas      uint64
	plans        uint64
}

func runFast2Ahead(t *testing.T, db kv.RwDB, fx *transferBlockFixture, on bool) fast2AheadOutcome {
	defer setTransferFast2ForTest(on)()
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(fx.cfg, bc, nil)
	blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
	var out fast2AheadOutcome
	plans0 := planTransfers.Load()
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		run, err := sp.ExecuteParallelAhead(blk, ibs, func(uint64) types.Hash { return types.Hash{} })
		if err != nil {
			return err
		}
		// The state the run left in ibs (FinishParallel's block-end system
		// calls need system contracts this state does not have).
		receipts := run.Receipts
		out.usedGas = run.UsedGas
		for i := range receipts {
			var buf bytes.Buffer
			receipts.EncodeIndex(i, &buf)
			out.receiptBytes = append(out.receiptBytes, buf.Bytes())
		}
		out.receiptsRoot = DeriveSha(receipts)
		out.stateRoot = ibs.IntermediateRoot()
		return nil
	}))
	out.plans = planTransfers.Load() - plans0
	return out
}

// TestTransferFast2Equivalence runs transfer blocks shaped like the bench's
// (sender chains and distinct senders, Zipf recipients) on the ahead path
// and the builder's path with N42_TRANSFER_FAST2 off and on, and requires
// the same state root, receipts and gas.
func TestTransferFast2Equivalence(t *testing.T) {
	for _, c := range []struct {
		name                 string
		txs, senders, recips int
		zipf, v              float64
	}{
		{"chains_zipf", 3000, 150, 400, 1.1, 2},
		{"distinct_zipf", 3000, 3000, 400, 1.2, 1},
		{"chains_modulo", 3000, 300, 97, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newTransferBlockFixture(c.txs, c.senders, c.recips, c.zipf, c.v)
			db := memdb.NewTestDB(t)
			fundTransferBlockStateFiller(t, db, fx, 1000)
			old := runFast2Ahead(t, db, fx, false)
			require.Zero(t, old.plans, "fast2=0 applied a plan")
			for round := 0; round < 3; round++ {
				got := runFast2Ahead(t, db, fx, true)
				require.Greater(t, got.plans, uint64(c.txs/2), "fast2 applied few plans")
				got.plans = 0
				o := old
				o.plans = 0
				require.Equal(t, o, got, "ahead run, round %d", round)
			}
			slow := runFastPathBlock(t, db, fx.cfg, fx.header, fx.txs, false)
			restore := setTransferFast2ForTest(false)
			b0 := runFastPathBlockMode(t, db, fx.cfg, fx.header, fx.txs, true, true)
			restore()
			restore = setTransferFast2ForTest(true)
			b1 := runFastPathBlockMode(t, db, fx.cfg, fx.header, fx.txs, true, true)
			restore()
			require.Equal(t, b0, b1, "builder run")
			require.Equal(t, slow.stateRoot, b1.stateRoot, "builder run against the EVM")
			require.Equal(t, slow.receiptBytes, b1.receiptBytes, "builder receipts against the EVM")
			require.Equal(t, old.stateRoot, b1.stateRoot, "ahead and builder state roots")
		})
	}
}

var (
	fast2FixtureOnce sync.Once
	fast2Fixture     *transferBlockFixture
)

// fast2BenchFixture is a block of distinct-sender transfers over Zipf
// recipients: N42_BENCH_TXS transfers (default 160,000), each from its own
// sender unless N42_BENCH_SENDERS says otherwise, to N42_BENCH_RECIPIENTS
// recipients (22,857) drawn from a Zipf distribution of exponent
// N42_BENCH_ZIPF/100 (default 1.1) and offset N42_BENCH_ZIPF_V (50): a hot
// set without one recipient taking a tenth of the block.
func fast2BenchFixture() *transferBlockFixture {
	fast2FixtureOnce.Do(func() {
		n := benchEnvInt("N42_BENCH_TXS", 160000)
		fast2Fixture = newTransferBlockFixture(n, benchEnvInt("N42_BENCH_SENDERS", n),
			benchEnvInt("N42_BENCH_RECIPIENTS", 22857), float64(benchEnvInt("N42_BENCH_ZIPF", 110))/100, float64(benchEnvInt("N42_BENCH_ZIPF_V", 50)))
	})
	return fast2Fixture
}

// BenchmarkDirectTransferWave runs the fast2 fixture's block through
// ExecuteParallelAhead on the old direct-transfer path and on fast2.
// Per transaction: waveCPUNs/tx is the process CPU of the executor's run
// (the exec wave and its validation; nothing else runs then), waveNs/tx the
// workers' wall time inside transaction bodies and their store writes, and
// cpuNs/tx the process CPU of the whole call.
func BenchmarkDirectTransferWave(b *testing.B) {
	fx := fast2BenchFixture()
	waveCPUMeter = processCPU
	defer func() { waveCPUMeter = nil }()
	db := memdb.NewTestDB(b)
	fundTransferBlockState(b, db, fx)
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(fx.cfg, bc, nil)
	blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	runOnce := func(b *testing.B) {
		roTx, err := db.BeginRo(context.Background())
		if err != nil {
			b.Fatalf("begin ro: %v", err)
		}
		defer roTx.Rollback()
		ibs := state.New(state.NewPlainStateReader(roTx))
		run, err := sp.ExecuteParallelAhead(blk, ibs, hashFn)
		if err != nil {
			b.Fatalf("ExecuteParallelAhead: %v", err)
		}
		if len(run.Receipts) != len(fx.txs) {
			b.Fatalf("receipts %d of %d", len(run.Receipts), len(fx.txs))
		}
	}
	for _, on := range []bool{false, true} {
		b.Run("fast2="+strconv.FormatBool(on), func(b *testing.B) {
			defer setTransferFast2ForTest(on)()
			runOnce(b) // warm-up: sender memos, pools
			d0 := directTransfers.Load()
			w0, c0 := waveWorkNs.Load(), waveCPUNs.Load()
			cpu0 := processCPU()
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				runOnce(b)
			}
			b.StopTimer()
			txs := float64(b.N * len(fx.txs))
			b.ReportMetric(float64(waveWorkNs.Load()-w0)/txs, "waveNs/tx")
			b.ReportMetric(float64(waveCPUNs.Load()-c0)/txs, "waveCPUNs/tx")
			b.ReportMetric(float64((processCPU()-cpu0).Nanoseconds())/txs, "cpuNs/tx")
			b.ReportMetric(float64(directTransfers.Load()-d0)/txs, "direct/tx")
		})
	}
}
