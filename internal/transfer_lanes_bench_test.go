// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

var (
	lanesBenchOnce    sync.Once
	lanesBenchFixture *transferBlockFixture
)

// lanesBenchShape is the bench's block: N42_BENCH_TXS transfers (326,000)
// from N42_BENCH_SENDERS senders (8,000; tx i is sender i%senders's nonce
// i/senders, ~41 per sender) to N42_BENCH_RECIPIENTS recipients (22,857,
// Zipf 1.1 offset 50), no sender a recipient.
func lanesBenchShape() *transferBlockFixture {
	lanesBenchOnce.Do(func() {
		lanesBenchFixture = newTransferBlockFixture(benchEnvInt("N42_BENCH_TXS", 326000), benchEnvInt("N42_BENCH_SENDERS", 8000),
			benchEnvInt("N42_BENCH_RECIPIENTS", 22857), 1.1, 50)
	})
	return lanesBenchFixture
}

// BenchmarkTransferLanesWave runs the bench-shaped block through
// ExecuteParallelAhead with the lanes off and on, on N42_PARALLEL_WORKERS
// workers (16 unless set). Per transaction: waveNs/tx is the wall time of
// the run's execution step (Executor.Run, or the lanes' four phases),
// waveCPUNs/tx the process CPU over that step, and runMs the step's wall
// time per block.
func BenchmarkTransferLanesWave(b *testing.B) {
	if os.Getenv("N42_PARALLEL_WORKERS") == "" {
		b.Setenv("N42_PARALLEL_WORKERS", "16")
	}
	fx := lanesBenchShape()
	waveCPUMeter = processCPU
	defer func() { waveCPUMeter = nil }()
	db := memdb.NewTestDB(b)
	fundTransferBlockStateFiller(b, db, fx, benchEnvInt("N42_BENCH_FILLER", 50000))
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(fx.cfg, bc, nil)
	blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	runOnce := func(b *testing.B) time.Duration {
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
		return run.phases.Exec
	}
	for _, mode := range []string{"off", "on"} {
		b.Run("lanes="+mode, func(b *testing.B) {
			defer SetTransferLanesForTest(mode)()
			runOnce(b) // warm-up: sender memos, pools, base cache paths
			u0, _, _, _ := TransferLanesStats()
			c0 := waveCPUNs.Load()
			var exec time.Duration
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				exec += runOnce(b)
			}
			b.StopTimer()
			u1, _, _, _ := TransferLanesStats()
			if mode == "on" && u1-u0 != uint64(b.N) {
				b.Fatalf("the lanes took %d of %d blocks", u1-u0, b.N)
			}
			txs := float64(b.N * len(fx.txs))
			b.ReportMetric(float64(exec.Nanoseconds())/txs, "waveNs/tx")
			b.ReportMetric(float64(waveCPUNs.Load()-c0)/txs, "waveCPUNs/tx")
			b.ReportMetric(float64(exec.Milliseconds())/float64(b.N), "runMs")
		})
	}
}
