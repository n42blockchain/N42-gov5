// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// prefetch.go — background MDBX page warm-up for the executor.
//
// prefetcher runs in a dedicated goroutine and consumes block numbers
// from the executor. While block N executes, it reads block N+1's body
// from the input freezer, recovers touched addresses and storage slots,
// and issues read-only MDBX lookups so the operating system page cache
// is primed ahead of the real execution pass. The channel is bounded to
// a tiny lookahead (2 blocks) to keep memory pressure predictable.

package ethel

import (
	"context"
	"sync"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// prefetcher warms MDBX page cache by pre-reading state data for upcoming blocks.
// Runs in a background goroutine. While block N executes, it reads addresses
// from block N+1's transactions and touches their MDBX pages.
//
// Senders: ecrecover at ~80μs/tx × 200+ tx/block = ~18 ms/block of CPU.
// At post-Constantinople blk/s rates this dominates the prefetcher's cycle
// time and starves it from doing the MDBX work that actually warms the
// cache. If the executor has pre-computed senders (segment store or
// freezer table), the prefetcher uses them too — falling back to
// ecrecover only on miss. Lookup is the same precedence as
// Executor.loadSenders.
type prefetcher struct {
	freezer  *freezer.Freezer
	db       kv.RoDB
	stateBuf *state.PlainStateBuffer
	chainCfg *params.ChainConfig
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	blockCh  chan uint64

	// Pre-computed senders sources, shared with the executor's main path.
	senderStore *SenderSegmentReader
	senderTable *freezer.FreezerTable
}

func newPrefetcher(ctx context.Context, f *freezer.Freezer, db kv.RoDB, buf *state.PlainStateBuffer, chainCfg *params.ChainConfig, senderStore *SenderSegmentReader, senderTable *freezer.FreezerTable) *prefetcher {
	ctx, cancel := context.WithCancel(ctx)
	return &prefetcher{
		freezer:     f,
		db:          db,
		stateBuf:    buf,
		chainCfg:    chainCfg,
		ctx:         ctx,
		cancel:      cancel,
		blockCh:     make(chan uint64, 2),
		senderStore: senderStore,
		senderTable: senderTable,
	}
}

func (p *prefetcher) start() {
	p.wg.Add(1)
	go p.loop()
}

func (p *prefetcher) stop() {
	p.cancel()
	p.wg.Wait()
}

func (p *prefetcher) prefetchBlock(blockNum uint64) {
	select {
	case p.blockCh <- blockNum:
	default:
	}
}

func (p *prefetcher) loop() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case blockNum := <-p.blockCh:
			p.doFetch(blockNum)
		}
	}
}

// loadSenders returns the pre-computed senders for blockNum, or nil if
// none of the configured sources can satisfy the request. Mirrors
// Executor.loadSenders precedence: SegmentStore → freezer table.
func (p *prefetcher) loadSenders(blockNum uint64, txCount int) []types.Address {
	if txCount == 0 {
		return nil
	}
	if p.senderStore != nil && blockNum < p.senderStore.MaxBlock() {
		if data, err := p.senderStore.ReadBlock(blockNum); err == nil {
			if n := len(data) / 20; n == txCount {
				out := make([]types.Address, n)
				for i := 0; i < n; i++ {
					copy(out[i][:], data[i*20:(i+1)*20])
				}
				return out
			}
		}
	}
	if p.senderTable != nil && blockNum < p.senderTable.Items() {
		if data, err := p.senderTable.Retrieve(blockNum); err == nil {
			if n := len(data) / 20; n == txCount {
				out := make([]types.Address, n)
				for i := 0; i < n; i++ {
					copy(out[i][:], data[i*20:(i+1)*20])
				}
				return out
			}
		}
	}
	return nil
}

func (p *prefetcher) doFetch(blockNum uint64) {
	data, err := p.freezer.Ancient(freezer.TableBodies, blockNum)
	if err != nil {
		return
	}
	body, err := DecodeGethBody(data)
	if err != nil {
		return
	}
	if len(body.Transactions) == 0 {
		return
	}

	// Senders: prefer the pre-computed source the executor uses; only
	// ecrecover (with the per-block signer) on cache miss. At ~80μs per
	// recovery × 200+ tx/block, ecrecover is what kept the prefetcher
	// from keeping up with exec on busy DeFi-era blocks.
	senders := p.loadSenders(blockNum, len(body.Transactions))

	addrs := make(map[types.Address]struct{}, len(body.Transactions)*3)
	if senders == nil {
		// Fallback path: full ecrecover. Build the signer once per block.
		headerData, err := p.freezer.Ancient(freezer.TableHeaders, blockNum)
		if err != nil {
			return
		}
		header, err := DecodeGethHeader(headerData)
		if err != nil {
			return
		}
		signer := transaction.MakeSigner(p.chainCfg, header.Number.ToBig())
		for _, tx := range body.Transactions {
			if sender, err := transaction.Sender(signer, tx); err == nil {
				addrs[sender] = struct{}{}
			}
		}
	} else {
		for _, s := range senders {
			addrs[s] = struct{}{}
		}
	}
	for _, tx := range body.Transactions {
		if to := tx.To(); to != nil {
			addrs[*to] = struct{}{}
		}
		for _, entry := range tx.AccessList() {
			addrs[entry.Address] = struct{}{}
		}
	}
	if len(addrs) == 0 {
		return
	}

	roTx, err := p.db.BeginRo(p.ctx)
	if err != nil {
		return
	}
	defer roTx.Rollback()

	// Read accounts from MDBX and populate the shared read cache.
	incarnations := make(map[types.Address]uint16, len(addrs))
	for addr := range addrs {
		enc, _ := roTx.GetOne(modules.Account, addr[:])
		if len(enc) > 0 {
			var a account.StateAccount
			if a.DecodeForStorage(enc) == nil {
				incarnations[addr] = 0
				p.stateBuf.CacheAccount(addr, &a)
			}
		} else {
			p.stateBuf.CacheAccount(addr, nil)
		}
	}

	// Prefetch storage slots and populate cache.
	for _, tx := range body.Transactions {
		for _, entry := range tx.AccessList() {
			for _, key := range entry.StorageKeys {
				compositeKey := modules.PlainGenerateCompositeStorageKey(entry.Address[:], key[:])
				enc, _ := roTx.GetOne(modules.Storage, compositeKey)
				var cached []byte
				if len(enc) > 0 {
					cached = make([]byte, len(enc))
					copy(cached, enc)
				}
				p.stateBuf.CacheStorage(entry.Address, key, cached)
			}
		}
	}
}
