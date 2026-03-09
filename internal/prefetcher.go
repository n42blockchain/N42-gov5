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

package internal

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// StatePrefetcher pre-loads state data from the database into cache before
// EVM execution begins. It parses transactions statically to predict which
// accounts and storage slots will be accessed, then reads them in background
// goroutines. When the real execution starts, those reads hit the cache
// instead of going to MDBX.
//
// The prefetcher uses the existing CachedStateReader mechanism — by issuing
// reads through a StateReader wrapped with CachedStateReader, the cache is
// populated as a side effect of the read.
type StatePrefetcher struct {
	config *params.ChainConfig
	wg     sync.WaitGroup
	cancel context.CancelFunc

	// Metrics.
	accountsPrefetched atomic.Int64
	storagePrefetched  atomic.Int64
	codePrefetched     atomic.Int64
}

// NewStatePrefetcher creates a new prefetcher.
func NewStatePrefetcher(config *params.ChainConfig) *StatePrefetcher {
	return &StatePrefetcher{config: config}
}

// Prefetch starts background goroutines that pre-load state for all
// transactions in the block. The stateReader should be a CachedStateReader
// (or chain: CachedStateReader → PlainStateReader) so that reads populate
// the shared cache.
//
// The prefetcher is non-blocking: it returns immediately. Call Close() to
// cancel and wait for completion.
func (p *StatePrefetcher) Prefetch(blk *block.Block, stateReader state.StateReader) {
	txs := blk.Transactions()
	if len(txs) == 0 {
		return
	}

	header, ok := blk.Header().(*block.Header)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	signer := transaction.MakeSigner(p.config, header.Number.ToBig())
	coinbase := header.Coinbase

	// Worker pool: bounded goroutines processing transactions.
	workers := runtime.NumCPU()
	if workers > len(txs) {
		workers = len(txs)
	}

	work := make(chan *transaction.Transaction, len(txs))

	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Warn("State prefetcher panic recovered", "err", r)
				}
			}()
			for tx := range work {
				if ctx.Err() != nil {
					return
				}
				p.prefetchTx(ctx, tx, signer, coinbase, stateReader)
			}
		}()
	}

	// Feed transactions to workers.
	go func() {
		for _, tx := range txs {
			select {
			case work <- tx:
			case <-ctx.Done():
				break
			}
		}
		close(work)
	}()
}

// prefetchTx pre-loads state for a single transaction.
func (p *StatePrefetcher) prefetchTx(ctx context.Context, tx *transaction.Transaction, signer transaction.Signer, coinbase types.Address, reader state.StateReader) {
	// 1. Sender account (always accessed for nonce/balance).
	sender, err := transaction.Sender(signer, tx)
	if err != nil {
		return // can't determine sender — skip
	}
	p.readAccount(ctx, reader, sender)

	// 2. Recipient account (nil for contract creation).
	if to := tx.To(); to != nil {
		acc := p.readAccount(ctx, reader, *to)
		// 3. If recipient has code, prefetch it.
		if acc != nil && acc.CodeHash != (types.Hash{}) {
			if ctx.Err() != nil {
				return
			}
			_, _ = reader.ReadAccountCode(*to, acc.Incarnation, acc.CodeHash)
			p.codePrefetched.Add(1)
		}
	}

	// 4. Access list entries (EIP-2930) — explicitly declared state access.
	if accessList := tx.AccessList(); len(accessList) > 0 {
		for _, tuple := range accessList {
			if ctx.Err() != nil {
				return
			}
			acc := p.readAccount(ctx, reader, tuple.Address)
			if acc == nil {
				continue
			}
			// Prefetch declared storage slots.
			for i := range tuple.StorageKeys {
				if ctx.Err() != nil {
					return
				}
				key := tuple.StorageKeys[i]
				_, _ = reader.ReadAccountStorage(tuple.Address, acc.Incarnation, &key)
				p.storagePrefetched.Add(1)
			}
		}
	}

	// 5. Coinbase account (accessed during reward distribution).
	p.readAccount(ctx, reader, coinbase)
}

// readAccount reads account data and returns it. Returns nil if not found or cancelled.
func (p *StatePrefetcher) readAccount(ctx context.Context, reader state.StateReader, addr types.Address) *account.StateAccount {
	if ctx.Err() != nil {
		return nil
	}
	acc, err := reader.ReadAccountData(addr)
	if err != nil {
		return nil
	}
	p.accountsPrefetched.Add(1)
	return acc
}

// Close cancels prefetching and waits for all goroutines to finish.
func (p *StatePrefetcher) Close() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()

	accounts := p.accountsPrefetched.Load()
	storage := p.storagePrefetched.Load()
	code := p.codePrefetched.Load()
	if accounts+storage+code > 0 {
		log.Debug("State prefetch completed",
			"accounts", accounts,
			"storage", storage,
			"code", code,
		)
	}
}

