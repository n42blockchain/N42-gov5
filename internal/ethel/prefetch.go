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
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
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

	// Speculative-prefetch wiring. engine + currentBlockNum are nil when
	// speculation is disabled, in which case doFetch falls back to the
	// static AccessList path.
	engine             consensus.Engine
	currentBlockNum    *atomic.Uint64
	speculativeEnabled bool
}

func newPrefetcher(ctx context.Context, f *freezer.Freezer, db kv.RoDB, buf *state.PlainStateBuffer, chainCfg *params.ChainConfig, senderStore *SenderSegmentReader, senderTable *freezer.FreezerTable, engine consensus.Engine, currentBlockNum *atomic.Uint64, speculativeEnabled bool) *prefetcher {
	ctx, cancel := context.WithCancel(ctx)
	return &prefetcher{
		freezer:            f,
		db:                 db,
		stateBuf:           buf,
		chainCfg:           chainCfg,
		ctx:                ctx,
		cancel:             cancel,
		blockCh:            make(chan uint64, 2),
		senderStore:        senderStore,
		senderTable:        senderTable,
		engine:             engine,
		currentBlockNum:    currentBlockNum,
		speculativeEnabled: speculativeEnabled,
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

	senders := p.loadSenders(blockNum, len(body.Transactions))

	// Speculative path needs the header for blockContext. Decode once and
	// reuse for both paths so the static AL fallback keeps working when
	// speculation is disabled or when senders are unavailable.
	var header *block.Header
	if p.speculativeEnabled || senders == nil {
		headerData, hErr := p.freezer.Ancient(freezer.TableHeaders, blockNum)
		if hErr != nil {
			return
		}
		header, err = DecodeGethHeader(headerData)
		if err != nil {
			return
		}
	}

	// Speculative prefetch: run the block's txs through a NoopWriter so
	// every read warms the cache. Falls back to static AL path on
	// missing senders (ecrecover during speculation would dominate CPU)
	// or for small blocks where the setup overhead exceeds the benefit.
	if p.speculativeEnabled && senders != nil && len(body.Transactions) >= SmallBlockTxThreshold {
		p.runSpeculative(blockNum, header, body, senders)
		return
	}

	addrs := make(map[types.Address]struct{}, len(body.Transactions)*3)
	if senders == nil {
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

	// Capture flushEpoch BEFORE BeginRo. CacheStorageIfAbsentEpoch
	// rejects the write if a flush stamps after this capture but
	// before the write — closes the stale-RoTx race that block
	// 2520771 hit pre-2026-04-28.
	prefetcherEpoch := p.stateBuf.CurrentFlushEpoch()
	roTx, err := p.db.BeginRo(p.ctx)
	if err != nil {
		return
	}
	defer roTx.Rollback()

	prewarmFromAccessList(p.stateBuf, roTx, addrs, body.Transactions, prefetcherEpoch)
}

// prewarmFromAccessList reads the accounts in addrs and the storage slots
// in txs' AccessLists from MDBX via roTx, populating S3-FIFO caches.
//
// LRU policy is asymmetric:
//
//   - Accounts are NEVER published. Active buf may hold a freshly-updated
//     StateAccount (new codeHash after CREATE, new balance after transfer)
//     while the bg flusher has not bumped flushEpoch. Prefetcher's RoTx
//     would read pre-update MDBX, IfAbsentEpoch guard would PASS (no flush
//     stamped), and a stale account would land in the LRU. Executor's next
//     LRU-first read returns the stale codeHash, EVM treats a fresh
//     contract as EOA — block 6411933 confirmed this failure mode
//     (n42=23320 gas vs geth=53513, 0 logs vs 1, status=1 in both). The
//     read is still issued so MDBX page-faults the leaf into OS page
//     cache (~free); the LRU write is the dangerous part.
//
//   - Storage IS published via CacheStorageIfAbsentEpoch. Safe because
//     bufReader intercepts stale slots via storageWipes / wipedStorage
//     before any value reaches consumers, and slot reads don't carry the
//     EOA-vs-contract behavioral cliff that codeHash does.
//
// This helper is the single chokepoint for the "no account LRU writes"
// invariant — exported package-private so a focused unit test can pin it.
func prewarmFromAccessList(buf *state.PlainStateBuffer, roTx kv.Tx, addrs map[types.Address]struct{}, txs []*transaction.Transaction, epoch uint64) {
	for addr := range addrs {
		_, _ = roTx.GetOne(modules.Account, addr[:])
	}
	for _, tx := range txs {
		for _, entry := range tx.AccessList() {
			for _, key := range entry.StorageKeys {
				compositeKey := modules.PlainGenerateCompositeStorageKey(entry.Address[:], key[:])
				val, err := roTx.GetOne(modules.Storage, compositeKey)
				if err != nil {
					continue
				}
				if len(val) > 0 {
					buf.CacheStorageIfAbsentEpoch(entry.Address, key, val, epoch)
				}
			}
		}
	}
}
