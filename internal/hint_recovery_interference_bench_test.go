// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// hintLoadTransactions pre-signs n DISTINCT legacy transfers (never reused
// across iterations, so each recovery is real -- the sender cache and the
// per-object memo are both keyed by hash, and a repeated hash would make
// this load free after the first pass instead of a genuine ~150k/s
// interference source).
func hintLoadTransactions(b *testing.B, n int) ([]*transaction.Transaction, transaction.Signer) {
	b.Helper()
	chainID := big.NewInt(94)
	signer := transaction.LatestSignerForChainID(chainID)
	to := types.Address{0xde, 0xad}
	txs := make([]*transaction.Transaction, n)
	for i := 0; i < n; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			b.Fatalf("key %d: %v", i, err)
		}
		tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{
			Nonce:    uint64(i),
			GasPrice: uint256.NewInt(10_000_000_000),
			Gas:      21000,
			To:       &to,
			Value:    uint256.NewInt(1),
		})
		if err != nil {
			b.Fatalf("sign %d: %v", i, err)
		}
		// Fresh decode: clears the per-object memo, so the FIRST touch of
		// each of these n transactions is a genuine, uncached recovery, same
		// as a wire-decoded hint-feed transaction.
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			b.Fatalf("encode %d: %v", i, err)
		}
		fresh, err := transaction.DecodeEthereumTransaction(enc)
		if err != nil {
			b.Fatalf("decode %d: %v", i, err)
		}
		txs[i] = fresh
	}
	return txs, signer
}

// startHintLoad runs a background sender-recovery load at approximately
// targetPerSec, cycling through a pre-signed pool (hintLoadTransactions) so
// it never runs out mid-benchmark, and returns a stop function. S59
// (docs/QS_BLOCK_TIME_BUDGET.md 6fa) part 3: this stands in for the
// fleet's own ingest hint feed, competing for the SAME GOMAXPROCS-wide
// OS-thread budget as BenchmarkParallelBlockTransfers's own executor
// goroutines -- there is no OS-level isolation in pure Go, which is exactly
// what 6f8's own "ingest hint-recovery CPU on the executor's shared pool"
// finding is about.
func startHintLoad(b *testing.B, targetPerSec int) (stop func()) {
	b.Helper()
	// One real recovery measured ~29us in isolation (6fa); at ~34.5k/s per
	// busy goroutine, ceil(targetPerSec/34500) goroutines running flat out
	// approximates targetPerSec without needing a precise rate limiter --
	// the interference number is what matters, not hitting the target
	// exactly.
	const perGoroutine = 34500
	workers := (targetPerSec + perGoroutine - 1) / perGoroutine
	if workers < 1 {
		workers = 1
	}
	txs, signer := hintLoadTransactions(b, 20000)

	stopCh := make(chan struct{})
	var idx atomic.Int64
	for w := 0; w < workers; w++ {
		go func() {
			for {
				select {
				case <-stopCh:
					return
				default:
				}
				i := idx.Add(1) % int64(len(txs))
				_, _ = transaction.RecoverSenderDeduped(signer, txs[i])
			}
		}()
	}
	return func() { close(stopCh) }
}

// BenchmarkParallelBlockTransfersInterference measures
// BenchmarkParallelBlockTransfers's own 20,000-transfer executor workload
// WHILE a ~150k/s background sender-recovery load runs concurrently (S59
// part 3). Compare its own ns/op directly against
// BenchmarkParallelBlockTransfers's (no load) -- the DELTA is the
// interference number 6f8 named at 31.8% of the executor's own profile.
func BenchmarkParallelBlockTransfersInterference(b *testing.B) {
	const (
		nSenders    = 20000
		nRecipients = 2857
		valuePerTx  = 1
		fundedWith  = uint64(1_000_000_000_000_000)
	)
	txs, senders := buildTransferBlockTxs(b, nSenders, nRecipients, valuePerTx)

	db := memdb.NewTestDB(b)
	fundBenchAccounts(b, db, senders, uint256.NewInt(fundedWith))
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(benchTransferConfig(), bc, nil)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		Time:       uint64(time.Now().Unix()),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(1),
		GasLimit:   1_000_000_000,
		Coinbase:   types.Address{0xC0, 0x1B, 0xAE},
	}

	runTransferBlockOnce(b, sp, bc, header, txs) // warm-up, outside the timed loop

	stop := startHintLoad(b, 150000)
	defer stop()
	// Let the load ramp to its own steady state before timing, so the first
	// measured iteration is not partly free of interference.
	time.Sleep(50 * time.Millisecond)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runTransferBlockOnce(b, sp, bc, header, txs)
	}
	b.StopTimer()
	b.ReportMetric(float64(nSenders), "txs/op")
}
