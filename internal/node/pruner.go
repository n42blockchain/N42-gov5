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

package node

import (
	"context"
	"encoding/binary"
	"time"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// SnapshotBoundary provides the oldest retained snapshot block number so
// that the pruner does not delete changesets still needed for snapshot reads.
type SnapshotBoundary interface {
	OldestRetainedBlock() uint64
}

// Pruner runs background pruning of historical blockchain data.
type Pruner struct {
	db     kv.RwDB
	config conf.PruneConfig
	hp     HealthProvider // used to get current block number
	snap   SnapshotBoundary
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	lastPrunedBlock uint64 // tracks the last block at which pruning was triggered
}

// NewPruner creates a new Pruner. Does not start pruning until Start is called.
// snap may be nil if snapshot system is disabled.
func NewPruner(db kv.RwDB, config conf.PruneConfig, hp HealthProvider, snap SnapshotBoundary) *Pruner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Pruner{
		db:     db,
		config: config,
		hp:     hp,
		snap:   snap,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

// Start begins the background pruning loop.
func (p *Pruner) Start() {
	go p.loop()
	log.Info("Pruner started",
		"mode", p.config.Mode,
		"retention", p.config.BlockRetention,
		"interval", p.config.PruneInterval,
		"batch_limit", p.config.PruneBatchLimit,
		"prune_receipts", p.config.PruneReceipts,
	)
}

// Stop signals the pruner to stop and waits for it to finish.
func (p *Pruner) Stop() {
	p.cancel()
	<-p.done
	log.Info("Pruner stopped")
}

// loop is the main pruning loop. It polls the current block number
// and triggers pruning when enough new blocks have been produced.
func (p *Pruner) loop() {
	defer close(p.done)

	// Poll every 10 seconds; pruning is only triggered when
	// currentBlock - lastPrunedBlock >= PruneInterval.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.maybePrune()
		}
	}
}

// maybePrune checks whether pruning should run and executes it.
func (p *Pruner) maybePrune() {
	currentBlock := p.hp.CurrentBlock()
	if currentBlock == 0 {
		return
	}

	// Guard against uint64 underflow (e.g. after reorg)
	if currentBlock <= p.lastPrunedBlock {
		return
	}

	// Only prune if enough new blocks have accumulated since last run
	if currentBlock-p.lastPrunedBlock < p.config.PruneInterval {
		return
	}

	// Compute the block boundary: prune everything older than this
	if currentBlock <= p.config.BlockRetention {
		return // not enough history yet
	}
	pruneTo := currentBlock - p.config.BlockRetention

	// Respect snapshot boundary: never prune changesets that are needed to
	// reconstruct state at the oldest retained snapshot height.
	if p.snap != nil {
		oldest := p.snap.OldestRetainedBlock()
		if oldest > 0 && pruneTo > oldest {
			pruneTo = oldest
		}
	}

	if err := p.prune(pruneTo); err != nil {
		log.Error("Pruning failed", "pruneTo", pruneTo, "err", err)
		return
	}

	p.lastPrunedBlock = currentBlock
}

// prune removes historical data older than pruneTo.
func (p *Pruner) prune(pruneTo uint64) error {
	start := time.Now()
	limit := p.config.PruneBatchLimit

	err := p.db.Update(p.ctx, func(tx kv.RwTx) error {
		logEvery := time.NewTicker(30 * time.Second)
		defer logEvery.Stop()

		// Prune account changesets (DupSort table)
		if err := rawdb.PruneTableDupSort(tx, modules.AccountChangeSet, "prune", pruneTo, logEvery, p.ctx); err != nil {
			return err
		}

		// Prune storage changesets (DupSort table)
		if err := rawdb.PruneTableDupSort(tx, modules.StorageChangeSet, "prune", pruneTo, logEvery, p.ctx); err != nil {
			return err
		}

		// Prune account history (regular table, key = address + shard_id)
		if err := pruneHistoryTable(tx, modules.AccountsHistory, pruneTo, limit, p.ctx); err != nil {
			return err
		}

		// Prune storage history (regular table, key = address + key + shard_id)
		if err := pruneHistoryTable(tx, modules.StorageHistory, pruneTo, limit, p.ctx); err != nil {
			return err
		}

		// Optionally prune receipts and logs
		if p.config.PruneReceipts {
			if err := rawdb.PruneTable(tx, modules.Receipts, pruneTo, p.ctx, limit); err != nil {
				return err
			}
			if err := rawdb.PruneTable(tx, modules.Log, pruneTo, p.ctx, limit); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	elapsed := time.Since(start)
	log.Info("Pruning completed", "pruneTo", pruneTo, "elapsed", elapsed.Truncate(time.Millisecond))
	return nil
}

// pruneHistoryTable prunes a history table where the last 8 bytes of the key
// are the shard ID (block number). We delete entries whose shard ID < pruneTo.
func pruneHistoryTable(tx kv.RwTx, table string, pruneTo uint64, limit int, ctx context.Context) error {
	c, err := tx.RwCursor(table)
	if err != nil {
		return err
	}
	defer c.Close()

	i := 0
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			return err
		}
		if i >= limit {
			break
		}

		// History table keys end with 8-byte shard ID (block number)
		if len(k) < 8 {
			continue
		}
		shardID := binary.BigEndian.Uint64(k[len(k)-8:])
		if shardID >= pruneTo {
			continue // keep this entry
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := c.DeleteCurrent(); err != nil {
			return err
		}
		i++
	}
	return nil
}

