// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Asynchronous transaction-lookup indexing.
//
// CommitToCanonical wrote one TxLookup row per transaction inside the write
// transaction that advances the committed head, on HotStuff's serial output
// loop. The rows themselves are cheap to stage -- 137 ms for 22,857 of them --
// but their keys are transaction hashes, so committing them means writing and
// fsyncing thousands of randomly-scattered B-tree pages. Measured across 193
// committed blocks at a 480M gas ceiling: 2,700 ms in mdbx_txn_commit against
// 149 ms of actual work and 4.8 ms waiting for the writer, and that single
// commit was about 77% of a 3.53 s block cycle.
//
// The index is not consensus state. Nothing in block validation, fork choice,
// or state execution reads it; it exists so eth_getTransactionByHash and
// eth_getTransactionReceipt can find a transaction without scanning. Writing
// it after the head has advanced, in its own transaction and batched across
// blocks, takes that cost off the critical path entirely.
//
// The cost of doing so is that a crash between the two writes leaves the index
// short of the chain. That is recoverable in a way a missing head is not: the
// entries can be rewritten from blocks that are already durable, and the
// idempotent re-index on the duplicate-commit path already does exactly that.

package internal

import (
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

const (
	// txIndexQueueDepth is how many committed blocks may await indexing. The
	// queue exists to absorb bursts, not to fall arbitrarily far behind: when
	// it fills, enqueueTxIndex indexes inline rather than dropping, so the
	// index can lag but never silently lose a block.
	txIndexQueueDepth = 64

	// txIndexBatchBlocks and txIndexBatchWait bound one indexing transaction.
	// Batching amortises the page writes that make this expensive in the first
	// place; the wait keeps a trickle of blocks from sitting unindexed.
	txIndexBatchBlocks = 8
	txIndexBatchWait   = 250 * time.Millisecond
)

// startTxIndexer launches the background indexer. Safe to call once.
func (bc *BlockChain) startTxIndexer() {
	if bc.txIndexCh != nil {
		return
	}
	bc.txIndexCh = make(chan *block.Block, txIndexQueueDepth)
	bc.txIndexDone = make(chan struct{})
	go bc.txIndexLoop()
}

// stopTxIndexer drains and stops the indexer. Draining matters: a node that
// exits with blocks still queued would leave gaps that nothing later fills.
func (bc *BlockChain) stopTxIndexer() {
	if bc.txIndexCh == nil {
		return
	}
	close(bc.txIndexCh)
	<-bc.txIndexDone
	bc.txIndexCh = nil
}

// enqueueTxIndex hands a committed block to the indexer. It never drops: if
// the queue is full the block is indexed inline, which is what the code did
// unconditionally before and is still correct, just slow.
func (bc *BlockChain) enqueueTxIndex(blk *block.Block) {
	if blk == nil || bc.txIndexCh == nil {
		if blk != nil {
			bc.writeTxIndexBatch([]*block.Block{blk})
		}
		return
	}
	select {
	case bc.txIndexCh <- blk:
	default:
		log.Warn("tx index queue full, indexing inline", "number", blk.Number64().Uint64())
		bc.writeTxIndexBatch([]*block.Block{blk})
	}
}

func (bc *BlockChain) txIndexLoop() {
	defer close(bc.txIndexDone)

	batch := make([]*block.Block, 0, txIndexBatchBlocks)
	timer := time.NewTimer(txIndexBatchWait)
	defer timer.Stop()
	if !timer.Stop() {
		<-timer.C
	}
	armed := false

	flush := func() {
		if len(batch) == 0 {
			return
		}
		bc.writeTxIndexBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case blk, ok := <-bc.txIndexCh:
			if !ok {
				flush() // drain on shutdown
				return
			}
			batch = append(batch, blk)
			if len(batch) >= txIndexBatchBlocks {
				flush()
				if armed && !timer.Stop() {
					<-timer.C
				}
				armed = false
				continue
			}
			if !armed {
				timer.Reset(txIndexBatchWait)
				armed = true
			}
		case <-timer.C:
			armed = false
			flush()
		}
	}
}

// writeTxIndexBatch writes the lookup entries for several blocks in one
// transaction.
func (bc *BlockChain) writeTxIndexBatch(blocks []*block.Block) {
	start := time.Now()
	txs := 0
	err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		for _, blk := range blocks {
			rawdb.WriteTxLookupEntries(tx, blk)
			txs += len(blk.Transactions())
		}
		return nil
	})
	if err != nil {
		// Loud: a missing index makes transactions unfindable by hash, which
		// looks to a user like the transaction was never mined.
		log.Error("tx index write failed", "blocks", len(blocks), "txs", txs, "err", err)
		return
	}
	if d := time.Since(start); d > time.Second {
		log.Info("tx index batch", "blocks", len(blocks), "txs", txs, "took", d)
	}
}
