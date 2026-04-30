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
		// Capacity 3: executor primes with N+1 on first iter then only
		// N+2 each subsequent iter. Worst-case pile-up = the
		// just-pre-fetched block + the in-flight one + a fresh
		// enqueue. See executor.go's 2-block lookahead.
		blockCh:            make(chan uint64, 3),
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
	if p.cancelled(blockNum) {
		return
	}
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

	// Speculative path needs the header for blockContext. Static-AL
	// fallback also needs it when senders are unavailable so we can run
	// ecrecover with the right signer for the block height. Decode
	// once.
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

	// Phase 1 — static AL prewarm. Runs UNCONDITIONALLY when senders
	// are available because it's cheap (~1-2 ms for typical blocks:
	// ~150 tx × ~3 reads = ~450 cursor SeekExact + LRU IfAbsent puts)
	// and it does two things speculative EVM does not by itself:
	//
	//   * Warms the OS page cache for B+tree leaves of every AL-listed
	//     storage slot, so the speculative pass below (and the real
	//     executor read after that) hits a warm page instead of cold
	//     NVMe seek.
	//   * Publishes AL-listed slot values into the readStorage LRU via
	//     CacheStorageIfAbsentEpoch. Speculative EVM's reads on those
	//     same slots become LRU hits (50-100 ns) instead of MDBX
	//     cursor descents (50-200 µs), so speculative completes within
	//     its budget more often → fewer cancellations → higher sto%.
	//
	// Skipped only when senders are nil (running ecrecover here would
	// dominate the prefetcher's per-block budget and starve Phase 2)
	// AND speculative is disabled — i.e. the legacy ecrecover-static
	// path. In that case we still walk the AL with ecrecover-resolved
	// senders to keep the prior fallback behaviour.
	if senders != nil {
		p.staticALPrewarm(blockNum, body, senders, nil)
	} else if !p.speculativeEnabled {
		signer := transaction.MakeSigner(p.chainCfg, header.Number.ToBig())
		p.staticALPrewarm(blockNum, body, nil, signer)
	}

	if p.cancelled(blockNum) {
		return
	}

	// Phase 2 — speculative EVM. Runs full block tx execution through a
	// NoopWriter so dynamically-computed (non-AL) SLOAD slots and
	// internal CALL targets also land in the LRU. Skipped on small
	// blocks (setup overhead exceeds benefit), missing senders (would
	// trigger ecrecover during speculation), or when speculative is
	// off. Phase 1's LRU prewarm makes this loop's reads ~10× cheaper.
	if p.speculativeEnabled && senders != nil && len(body.Transactions) >= SmallBlockTxThreshold {
		p.runSpeculative(blockNum, header, body, senders)
	}
}

// staticALPrewarm collects sender + to + AL addresses and runs the
// cheap MDBX-only prewarm. signer is used for ecrecover when senders
// are not pre-computed; pass nil when senders is non-nil.
func (p *prefetcher) staticALPrewarm(blockNum uint64, body *GethBodyResult, senders []types.Address, signer transaction.Signer) {
	addrs := make(map[types.Address]struct{}, len(body.Transactions)*3)
	if senders != nil {
		for _, s := range senders {
			addrs[s] = struct{}{}
		}
	} else if signer != nil {
		for _, tx := range body.Transactions {
			if sender, err := transaction.Sender(signer, tx); err == nil {
				addrs[sender] = struct{}{}
			}
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
