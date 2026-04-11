// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// State prefetcher. Runs concurrently with block execution,
// speculatively loading accounts and storage slots the predicted
// transaction workload will touch. Uses a context cancellation
// hook so the prefetch goroutines exit cleanly when a block
// commits or aborts.

package internal

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// prefetchRequestType distinguishes different kinds of prefetch requests.
type prefetchRequestType uint8

const (
	prefetchAccount prefetchRequestType = iota
	prefetchStorage
	prefetchCode
)

// prefetchRequest is a single I/O request dispatched to the async I/O pool.
type prefetchRequest struct {
	typ     prefetchRequestType
	address types.Address
	slot    types.Hash // only for prefetchStorage
}

// StatePrefetcher pre-loads state data from the database into cache before
// EVM execution begins. Request generation (parsing transactions) is decoupled
// from I/O execution (MDBX reads) via a bounded channel, enabling the I/O pool
// to batch and overlap reads independently of the parsing goroutines.
type StatePrefetcher struct {
	config    *params.ChainConfig
	predictor *PrefetchPredictor
	genWg     sync.WaitGroup      // tracks request-generation goroutines
	ioWg      sync.WaitGroup      // tracks I/O worker goroutines
	cancel    context.CancelFunc
	ioChan    chan prefetchRequest // bounded channel for async dispatch

	// Metrics.
	accountsPrefetched  atomic.Int64
	storagePrefetched   atomic.Int64
	codePrefetched      atomic.Int64
	predictedPrefetched atomic.Int64
}

// NewStatePrefetcher creates a new prefetcher.
func NewStatePrefetcher(config *params.ChainConfig) *StatePrefetcher {
	return &StatePrefetcher{config: config}
}

// SetPredictor enables predictive slot prefetching based on historical access patterns.
func (p *StatePrefetcher) SetPredictor(pred *PrefetchPredictor) {
	p.predictor = pred
}

// Prefetch starts background goroutines that pre-load state for all
// transactions in the block. The stateReader should be a CachedStateReader
// (or chain: CachedStateReader → PlainStateReader) so that reads populate
// the shared cache.
//
// Architecture: request generators parse transactions and submit prefetchRequest
// structs to a bounded channel. A separate I/O pool drains the channel and
// performs actual MDBX reads.
func (p *StatePrefetcher) Prefetch(blk *block.Block, stateReader state.StateReader) {
	txs := blk.Transactions()
	if len(txs) == 0 {
		return
	}

	header, ok := blk.Header().(*block.Header)
	if !ok {
		return
	}
	if _, err := requireHeaderNumber(header, "header number unavailable"); err != nil {
		return
	}

	// Cancel any previous prefetch to avoid goroutine leak.
	if p.cancel != nil {
		p.cancel()
		p.genWg.Wait()
		p.ioWg.Wait()
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	signer := transaction.MakeSignerWithTimestamp(p.config, header.Number.ToBig(), header.Time)
	coinbase := header.Coinbase

	// I/O channel: bounded to decouple request generation from I/O.
	chanSize := len(txs) * 3 // ~3 requests per tx (sender + recipient + coinbase)
	if chanSize > 4096 {
		chanSize = 4096
	}
	p.ioChan = make(chan prefetchRequest, chanSize)

	// Start I/O pool: fewer goroutines than generators.
	ioWorkers := runtime.NumCPU() / 2
	if ioWorkers < 2 {
		ioWorkers = 2
	}
	for i := 0; i < ioWorkers; i++ {
		p.ioWg.Add(1)
		go p.ioWorker(ctx, stateReader)
	}

	// Request generation workers.
	genWorkers := runtime.NumCPU()
	if genWorkers > len(txs) {
		genWorkers = len(txs)
	}

	work := make(chan *transaction.Transaction, len(txs))

	for i := 0; i < genWorkers; i++ {
		p.genWg.Add(1)
		go func() {
			defer p.genWg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Warn("State prefetcher panic recovered", "err", r)
				}
			}()
			for tx := range work {
				if ctx.Err() != nil {
					return
				}
				p.generateRequests(ctx, tx, signer, coinbase)
			}
		}()
	}

	// Feed transactions to generators, then close ioChan when all generators finish.
	go func() {
	feed:
		for _, tx := range txs {
			select {
			case work <- tx:
			case <-ctx.Done():
				break feed
			}
		}
		close(work)
		p.genWg.Wait()
		close(p.ioChan)
	}()
}

// generateRequests parses a transaction and submits prefetch requests to the I/O channel.
// This method does NOT perform any I/O — it only generates request descriptors.
func (p *StatePrefetcher) generateRequests(ctx context.Context, tx *transaction.Transaction, signer transaction.Signer, coinbase types.Address) {
	sender, err := transaction.Sender(signer, tx)
	if err != nil {
		return
	}

	p.submit(ctx, prefetchRequest{typ: prefetchAccount, address: sender})

	if to := tx.To(); to != nil {
		p.submit(ctx, prefetchRequest{typ: prefetchAccount, address: *to})
		p.submit(ctx, prefetchRequest{typ: prefetchCode, address: *to})
	}

	if accessList := tx.AccessList(); len(accessList) > 0 {
		for _, tuple := range accessList {
			p.submit(ctx, prefetchRequest{typ: prefetchAccount, address: tuple.Address})
			for i := range tuple.StorageKeys {
				p.submit(ctx, prefetchRequest{
					typ:     prefetchStorage,
					address: tuple.Address,
					slot:    tuple.StorageKeys[i],
				})
			}
		}
	}

	p.submit(ctx, prefetchRequest{typ: prefetchAccount, address: coinbase})

	// Predictive prefetching: pre-load historically hot storage slots.
	if p.predictor != nil && tx.To() != nil {
		predicted := p.predictor.PredictSlots(*tx.To(), 8)
		p.predictedPrefetched.Add(int64(len(predicted)))
		for _, slot := range predicted {
			p.submit(ctx, prefetchRequest{
				typ:     prefetchStorage,
				address: *tx.To(),
				slot:    slot,
			})
		}
	}
}

// submit sends a prefetch request to the I/O channel, respecting cancellation.
func (p *StatePrefetcher) submit(ctx context.Context, req prefetchRequest) {
	select {
	case p.ioChan <- req:
	case <-ctx.Done():
	}
}

// ioWorker drains prefetch requests from the channel and performs actual MDBX reads.
func (p *StatePrefetcher) ioWorker(ctx context.Context, reader state.StateReader) {
	defer p.ioWg.Done()
	defer func() {
		if r := recover(); r != nil {
			log.Warn("Prefetch I/O worker panic recovered", "err", r)
		}
	}()

	for req := range p.ioChan {
		if ctx.Err() != nil {
			return
		}

		switch req.typ {
		case prefetchAccount:
			_, _ = reader.ReadAccountData(req.address)
			p.accountsPrefetched.Add(1)

		case prefetchStorage:
			acc, _ := reader.ReadAccountData(req.address)
			if acc != nil {
				slot := req.slot
				_, _ = reader.ReadAccountStorage(req.address, 0, &slot)
				p.storagePrefetched.Add(1)
			}

		case prefetchCode:
			acc, _ := reader.ReadAccountData(req.address)
			if acc != nil && acc.CodeHash != (types.Hash{}) {
				_, _ = reader.ReadAccountCode(req.address, 0, acc.CodeHash)
				p.codePrefetched.Add(1)
			}
		}
	}
}

// Close cancels prefetching and waits for all goroutines to finish.
func (p *StatePrefetcher) Close() {
	if p.cancel != nil {
		p.cancel()
	}
	p.genWg.Wait()
	p.ioWg.Wait()

	accounts := p.accountsPrefetched.Load()
	storage := p.storagePrefetched.Load()
	code := p.codePrefetched.Load()
	predicted := p.predictedPrefetched.Load()
	if accounts+storage+code+predicted > 0 {
		log.Debug("State prefetch completed",
			"accounts", accounts,
			"storage", storage,
			"code", code,
			"predicted", predicted,
		)
	}
}
