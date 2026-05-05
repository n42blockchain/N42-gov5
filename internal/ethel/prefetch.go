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

	// Optional N42 columnar readers — when non-nil, prefetch uses these
	// instead of geth-format Ancient bytes for headers/bodies. Same
	// precedence as Executor.readHeader/readBody.
	compactHeaders *HeaderCompactReader
	compactBodies  *BodyCompactReader

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

// SetCompactReaders mirrors Executor.SetCompactReaders so prefetch
// reads from the same columnar archive when geth ancient is missing
// or stale.
func (p *prefetcher) SetCompactReaders(hr *HeaderCompactReader, br *BodyCompactReader) {
	p.compactHeaders = hr
	p.compactBodies = br
}

func (p *prefetcher) fetchHeader(blockNum uint64) (*block.Header, error) {
	if p.compactHeaders != nil {
		return p.compactHeaders.ReadHeader(blockNum)
	}
	data, err := p.freezer.Ancient(freezer.TableHeaders, blockNum)
	if err != nil {
		return nil, err
	}
	return DecodeGethHeader(data)
}

func (p *prefetcher) fetchBody(blockNum uint64) (*GethBodyResult, error) {
	if p.compactBodies != nil {
		db, err := p.compactBodies.ReadBody(blockNum)
		if err != nil {
			return nil, err
		}
		return &GethBodyResult{
			Transactions: db.Txs,
			Withdrawals:  db.Withdrawals,
		}, nil
	}
	data, err := p.freezer.Ancient(freezer.TableBodies, blockNum)
	if err != nil {
		return nil, err
	}
	return DecodeGethBody(data)
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
	body, err := p.fetchBody(blockNum)
	if err != nil || body == nil {
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
		header, err = p.fetchHeader(blockNum)
		if err != nil {
			return
		}
	}

	// Phase 1 — static AL prewarm. Always runs:
	//
	//   * senders != nil (common mainnet path with sender-recovery
	//     output present): cheap (~1-2 ms for typical blocks, ~150 tx
	//     × ~3 reads = ~450 cursor SeekExact + LRU IfAbsent puts).
	//     Two effects beyond cache fill:
	//       a. Warms the OS page cache for B+tree leaves of every
	//          AL-listed storage slot, so the speculative pass below
	//          (and the real executor read after that) hits a warm
	//          page instead of cold NVMe seek.
	//       b. Publishes AL-listed slot values into the readStorage
	//          LRU via CacheStorageIfAbsentEpoch. Speculative EVM's
	//          reads on those same slots become LRU hits (50-100 ns)
	//          instead of MDBX cursor descents (50-200 µs), so
	//          speculative completes within its budget more often →
	//          fewer cancellations → higher sto%.
	//
	//   * senders == nil (sender-recovery has not run for this block):
	//     fall back to ecrecover for senders. The cost is real
	//     (~80 µs/tx × 200 tx = ~16 ms) but skipping prefetch entirely
	//     leaves the executor with a cold cache for the whole block —
	//     measured worse on a senders-missing run (sto% dropped from
	//     94% → ~70%) because the speculative path also requires
	//     senders, so it would skip too. Ecrecover here is a one-time
	//     cost; the LRU fill it produces benefits both the prefetched
	//     speculative pass (if eligible) and the real executor read.
	//
	// Mirrors the pre-2026-04-30 fallback behaviour while preserving
	// the new "Phase 1 always primes Phase 2" property.
	if senders != nil {
		p.staticALPrewarm(blockNum, body, senders, nil)
	} else {
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

	roTx, err := p.db.BeginRo(p.ctx)
	if err != nil {
		return
	}
	defer roTx.Rollback()

	prewarmFromAccessList(roTx, addrs, body.Transactions)
}

// prewarmFromAccessList page-faults the MDBX leaves for every address
// in addrs and every (entry.Address, key) pair in txs' AccessLists.
// LRU writes are intentionally omitted — see prefetchStateReader for
// the race that motivated removing them; main-thread prewarmStaticAL
// is now the LRU-warming path.
func prewarmFromAccessList(roTx kv.Tx, addrs map[types.Address]struct{}, txs []*transaction.Transaction) {
	for addr := range addrs {
		_, _ = roTx.GetOne(modules.Account, addr[:])
	}
	for _, tx := range txs {
		for _, entry := range tx.AccessList() {
			for _, key := range entry.StorageKeys {
				compositeKey := modules.PlainGenerateCompositeStorageKey(entry.Address[:], key[:])
				_, _ = roTx.GetOne(modules.Storage, compositeKey)
			}
		}
	}
}
