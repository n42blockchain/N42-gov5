// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// N42_INGEST_WORKERS: spreads a raw-transaction batch's own decode + ECDSA
// sender recovery over a bounded worker pool instead of one goroutine doing
// all of it serially (unset/<=1 = today).
//
// S53 (docs/QS_BLOCK_TIME_BUDGET.md 6f6): eight generators offering 20,000
// tx/s each delivered only ~16.8k tx/s each -- consistent with EACH
// connection's own BatchRawTransaction call being serviced by its own
// goroutine (concurrency ACROSS connections is not the bottleneck; eight of
// them together clear ~130-140k tx/s), but the 200-entry loop INSIDE one
// call recovering senders one at a time, ~50us of pure ECDSA CPU each --
// about 10ms of a ~13ms batch, one core's worth regardless of how many
// cores the box has free. The pool's own addTxs (internal/txspool/txs_pool.go)
// already parallelizes ITS OWN recovery (prewarmSenders,
// internal/txspool/sender_prewarm.go) across up to GOMAXPROCS-1 workers, but
// by the time a batch gets there every sender is already cached by
// seedRecoveredSender, so that parallel pass is a memo hit and buys nothing
// here -- the uncached work is the RPC handler's own serial loop.
//
// The pool is process-wide, not one-per-call: N42_INGEST_WORKERS bounds how
// much of the node's own CPU decode+recovery may use in total, regardless of
// how many generators are submitting batches at once (a per-call pool would
// let N connections each spin up N42_INGEST_WORKERS of their own, multiplying
// unboundedly).
package api

import (
	"os"
	"strconv"
	"sync"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
)

var (
	ingestWorkersOnce sync.Once
	ingestWorkersN    int
	ingestJobsShared  chan func()
)

// newIngestPool starts n long-lived worker goroutines reading from a fresh
// job channel and returns it. Factored out of ingestWorkers so a test or
// benchmark can create its OWN pool at whatever size it wants, independent
// of the process-wide singleton (which is sized once, from the environment,
// for the process's whole lifetime).
func newIngestPool(n int) chan func() {
	jobs := make(chan func(), n*4)
	for i := 0; i < n; i++ {
		go func() {
			for job := range jobs {
				job()
			}
		}()
	}
	return jobs
}

// ingestWorkers returns the configured worker count (0 = off, today's
// serial behaviour) and the shared, process-wide pool to dispatch onto (nil
// when off), parsed and started once.
func ingestWorkers() (int, chan func()) {
	ingestWorkersOnce.Do(func() {
		v := os.Getenv("N42_INGEST_WORKERS")
		if v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 1 {
			return
		}
		ingestWorkersN = n
		ingestJobsShared = newIngestPool(n)
	})
	return ingestWorkersN, ingestJobsShared
}

// ingestResult is one batch entry's outcome: either a decoded, validated,
// sender-recovered, fee-checked transaction ready for the pool, or the
// error that entry failed with. Identical to what the serial loop produced
// per entry before this existed.
type ingestResult struct {
	tx  *transaction.Transaction
	err error
}

// processBatchEntries runs processOne(i, inputs[i]) for every entry.
//
// workers<=1, fewer than 2 inputs, or a nil jobs channel: runs serially, in
// order, index 0 to len(inputs)-1 -- byte-for-byte today's loop, just
// factored out.
//
// workers>1: dispatches each entry as a job on jobs and waits for all of
// them. Every goroutine writes only to its OWN index of results, so there is
// no need for a lock around the writes; results are assembled by the caller
// in index order afterwards, giving the exact same "first error, in original
// batch order" semantics as the serial loop even though the WORK finishes in
// an arbitrary order.
func processBatchEntries(inputs []hexutil.Bytes, workers int, jobs chan func(), processOne func(i int, t hexutil.Bytes) (*transaction.Transaction, error)) []ingestResult {
	results := make([]ingestResult, len(inputs))
	if workers <= 1 || jobs == nil || len(inputs) < 2 {
		for i, t := range inputs {
			tx, err := processOne(i, t)
			results[i] = ingestResult{tx: tx, err: err}
		}
		return results
	}
	var wg sync.WaitGroup
	wg.Add(len(inputs))
	for i, t := range inputs {
		i, t := i, t
		jobs <- func() {
			defer wg.Done()
			tx, err := processOne(i, t)
			results[i] = ingestResult{tx: tx, err: err}
		}
	}
	wg.Wait()
	return results
}
