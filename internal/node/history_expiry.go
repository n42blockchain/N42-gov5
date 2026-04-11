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
// History expiry policy. Periodically prunes ancient blocks, receipts
// and log indexes that fall outside the configured retention window,
// coordinating with the freezer so only compressed, append-only
// history is kept long term. Driven by a background goroutine on the
// Node.

package node

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// HistoryExpirer implements EIP-4444 style history expiry. It periodically
// removes old block headers, bodies, receipts, logs, senders, and total
// difficulty entries that fall behind the chain head by more than the
// configured retention window.
//
// Unlike the Pruner (which targets changeset/history tables), the
// HistoryExpirer targets chain-level block data tables.
type HistoryExpirer struct {
	db     kv.RwDB
	config conf.HistoryExpiryConfig
	hp     HealthProvider

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	// earliestBlock is the cached earliest available block number.
	// Updated atomically after each successful expiry cycle and persisted
	// in the DB so it survives restarts.
	earliestBlock atomic.Uint64

	lastExpiredAt uint64 // block height at which we last ran expiry
}

// NewHistoryExpirer creates a new HistoryExpirer. Call Start to begin the
// background expiry loop.
func NewHistoryExpirer(db kv.RwDB, config conf.HistoryExpiryConfig, hp HealthProvider) *HistoryExpirer {
	config.Validate()
	ctx, cancel := context.WithCancel(context.Background())
	e := &HistoryExpirer{
		db:     db,
		config: config,
		hp:     hp,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	// Load persisted earliest block from DB.
	if err := db.View(ctx, func(tx kv.Tx) error {
		val, err := rawdb.ReadEarliestBlock(tx)
		if err != nil {
			return err
		}
		e.earliestBlock.Store(val)
		return nil
	}); err != nil {
		log.Warn("HistoryExpirer: failed to read earliest block from DB", "err", err)
	}

	return e
}

// Start begins the background expiry loop.
func (e *HistoryExpirer) Start() {
	go e.loop()
	log.Info("HistoryExpirer started",
		"retention", e.config.Retention,
		"interval", e.config.Interval,
		"batch_limit", e.config.BatchLimit,
		"earliest_block", e.earliestBlock.Load(),
	)
}

// Stop signals the expirer to stop and waits for it to finish.
func (e *HistoryExpirer) Stop() {
	e.cancel()
	<-e.done
	log.Info("HistoryExpirer stopped")
}

// EarliestBlock returns the earliest block number that still has full data
// available. Returns 0 if no expiry has happened yet.
func (e *HistoryExpirer) EarliestBlock() uint64 {
	return e.earliestBlock.Load()
}

// loop is the main expiry loop. It polls every 10 seconds and triggers
// expiry when enough new blocks have accumulated.
func (e *HistoryExpirer) loop() {
	defer close(e.done)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.maybeExpire()
		}
	}
}

// maybeExpire checks whether expiry should run and executes it.
func (e *HistoryExpirer) maybeExpire() {
	currentBlock := e.hp.CurrentBlock()
	if currentBlock == 0 {
		return
	}

	// Guard against uint64 underflow after reorg.
	if currentBlock <= e.lastExpiredAt {
		return
	}

	// Only expire if enough new blocks have accumulated since last run.
	if currentBlock-e.lastExpiredAt < e.config.Interval {
		return
	}

	// Compute the expiry boundary: expire everything older than this.
	if currentBlock <= e.config.Retention {
		return // not enough history yet
	}
	expireTo := currentBlock - e.config.Retention

	// Never expire below our current earliest block.
	earliest := e.earliestBlock.Load()
	if earliest >= expireTo {
		return
	}

	if err := e.expire(expireTo); err != nil {
		log.Error("History expiry failed", "expireTo", expireTo, "err", err)
		return
	}

	e.lastExpiredAt = currentBlock
}

// expire deletes block-level data for blocks in [earliestBlock, expireTo).
// It processes at most config.BatchLimit blocks per call to avoid holding the
// write transaction for too long.
func (e *HistoryExpirer) expire(expireTo uint64) error {
	start := time.Now()
	from := e.earliestBlock.Load()
	if from == 0 {
		// First run: start from block 1 (never expire genesis).
		from = 1
	}

	// Clamp to batch limit.
	batchEnd := from + uint64(e.config.BatchLimit)
	if batchEnd > expireTo {
		batchEnd = expireTo
	}

	deleted := 0

	err := e.db.Update(e.ctx, func(tx kv.RwTx) error {
		for blockNum := from; blockNum < batchEnd; blockNum++ {
			select {
			case <-e.ctx.Done():
				return e.ctx.Err()
			default:
			}

			// Look up canonical hash for this block number.
			hash, err := rawdb.ReadCanonicalHash(tx, blockNum)
			if err != nil {
				return err
			}
			if hash == (types.Hash{}) {
				// No canonical hash for this block, skip.
				continue
			}

			if err := rawdb.DeleteBlockData(tx, blockNum, hash); err != nil {
				return err
			}
			deleted++
		}

		// Persist the new earliest block.
		if err := rawdb.WriteEarliestBlock(tx, batchEnd); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		return err
	}

	e.earliestBlock.Store(batchEnd)

	elapsed := time.Since(start)
	log.Info("History expiry completed",
		"from", from,
		"to", batchEnd,
		"deleted", deleted,
		"elapsed", elapsed.Truncate(time.Millisecond),
	)
	return nil
}
