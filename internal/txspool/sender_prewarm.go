// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Parallel sender recovery for an incoming batch.
//
// Recovering a transaction's sender is an ECDSA public-key recovery: about
// 50 µs, pure CPU, dependent on nothing but the transaction. It was done one
// transaction at a time inside addTxs' validation loop, which made the pool's
// ingest path as narrow as a single core no matter how many were free -- a
// fleet node at saturation used 4 of 32 cores, with ~40% of its profile in
// runtime scheduler park/spin rather than work.
//
// Recovering the batch first turns that loop's recoveries into memo hits.
// Nothing here decides anything: a recovery that fails is simply left for
// validateSender to fail on in the usual place, so parallelism cannot change a
// verdict, only when it is reached.

package txspool

import (
	"runtime"
	"sync"

	"github.com/n42blockchain/N42/common/transaction"
)

// prewarmSenderMinBatch is the batch size below which recovering serially is
// cheaper than starting workers.
const prewarmSenderMinBatch = 8

// prewarmSenderMaxWorkers bounds the worker count. Ingest is not the only
// thing running: leaving headroom keeps a large batch from starving block
// execution and consensus on the same host.
var prewarmSenderMaxWorkers = func() int {
	n := runtime.NumCPU() - 2
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	return n
}()

// prewarmSenders recovers the senders of txs concurrently, populating each
// transaction's memo (and the process-wide sender cache) so the caller's
// validation pass does no elliptic-curve work.
//
// Transactions already held by the pool are skipped: they were recovered when
// they were first admitted, and re-recovering them is exactly the waste this
// exists to remove.
func (pool *TxsPool) prewarmSenders(txs []*transaction.Transaction) {
	if len(txs) < prewarmSenderMinBatch || pool.chainconfig == nil {
		return
	}
	signer := transaction.LatestSignerForChainID(pool.chainconfig.ChainID)

	workers := prewarmSenderMaxWorkers
	if workers > len(txs) {
		workers = len(txs)
	}
	if workers < 2 {
		return // nothing to gain
	}

	var (
		wg   sync.WaitGroup
		next = make(chan *transaction.Transaction, workers)
	)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for tx := range next {
				// Errors are deliberately dropped: this is a cache warm-up,
				// and validateSender re-runs the same call and reports the
				// failure with all its surrounding checks.
				_, _ = transaction.Sender(signer, tx)
			}
		}()
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if pool.all.Get(tx.Hash()) != nil {
			continue // already admitted; its sender is already recovered
		}
		next <- tx
	}
	close(next)
	wg.Wait()
}
