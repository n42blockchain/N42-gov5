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

	// Capture flushEpoch BEFORE BeginRo. If a flush completes between
	// capture and BeginRo, prefetcherEpoch underestimates (older), and
	// CacheStorageIfAbsentEpoch will conservatively reject any value
	// for an address wiped by that interim flush — exactly the safety
	// invariant we need.
	prefetcherEpoch := p.stateBuf.CurrentFlushEpoch()
	roTx, err := p.db.BeginRo(p.ctx)
	if err != nil {
		return
	}
	defer roTx.Rollback()

	// Prewarm OS page cache + LRU for upcoming blocks via Cache*IfAbsentEpoch.
	//
	// This prefetcher runs concurrently with the executor + background
	// flusher. roTx's MDBX snapshot may lag behind the latest flush —
	// reading from it can return values older than what
	// RefreshLRUForSnapshot just wrote into LRU. We must NEVER overwrite
	// the LRU because the executor's flush is the canonical writer; we
	// can only fill empty slots.
	//
	// Cache*IfAbsent is the tombstone-safe primitive: it writes only if
	// the key is absent, returning false (no-op) when an entry already
	// exists. This preserves the warmth benefit of populating cold-LRU
	// slots while making the previous data race (block 17,150,008 stale
	// resurrection) impossible: even if our roTx is stale, our value
	// can't displace whatever RefreshLRU has already canonicalized.
	//
	// MDBX GetOne also pulls the underlying B-tree pages into the OS
	// page cache, helping subsequent reads even when the LRU write was
	// a no-op (key already present).
	for addr := range addrs {
		// MDBX GetOne pulls the addr's B-tree pages into the OS page
		// cache — that's the only prefetch benefit we want. We do NOT
		// publish the bytes into PlainStateBuffer.readAccounts here.
		//
		// Why not: the prefetcher RoTx is captured BEFORE the next
		// commit interval's flush, so its read can return a stale
		// pre-flush balance. CacheAccountIfAbsentEpoch's wipedAtEpoch
		// guard only fires for SELFDESTRUCT'd addresses (the only ones
		// StampWipes records). For ordinary transfers / coinbase /
		// SELFDESTRUCT-beneficiary credits the guard is a no-op, so a
		// stale put can land into an LRU slot that was evicted between
		// RefreshLRUForSnapshot's overwrite and the prefetcher's
		// arrival. The executor's next read then returns the stale
		// pre-credit balance and reports "insufficient funds for
		// gas * price + value" on a tx that should have succeeded —
		// observed at block 2520771 (sender 0xB8cc0F060AAd92d4eb8B36b3B95cE9E90eb383d7,
		// missing exactly 150,000 ETH from a prior in-flight credit).
		// d:/N42-eth25m happened to never evict the sender's hot LRU
		// entry so it cleared the same block; a fresh-genesis replay
		// hits the eviction and fails deterministically.
		//
		// Storage caching below is left in place: StampWipes covers
		// SELFDESTRUCT'd address slots (the original block-10941141
		// zombie family), and slot values change far less frequently
		// per address than balances do, so the eviction-then-stale-put
		// race surfaces orders of magnitude less often. If a similar
		// production divergence ever pins a storage slot to this
		// pattern we should drop the storage put too and rely on
		// bufReader's on-demand cache fill.
		_, _ = roTx.GetOne(modules.Account, addr[:])
	}
	for _, tx := range body.Transactions {
		for _, entry := range tx.AccessList() {
			for _, key := range entry.StorageKeys {
				compositeKey := modules.PlainGenerateCompositeStorageKey(entry.Address[:], key[:])
				val, err := roTx.GetOne(modules.Storage, compositeKey)
				if err != nil {
					continue
				}
				// Same reasoning as account: skip negative cache. A slot
				// that's zero in our pinned MDBX RoTx may be non-zero in
				// the in-flight or just-flushed snap.
				if len(val) > 0 {
					p.stateBuf.CacheStorageIfAbsentEpoch(entry.Address, key, val, prefetcherEpoch)
				}
			}
		}
	}
}
