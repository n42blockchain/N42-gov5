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

package snapshot

import (
	"context"
	"time"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// BlockProvider abstracts the current chain head for the snapshot manager.
type BlockProvider interface {
	CurrentBlock() uint64
}

// Manager periodically creates logical state snapshots. A snapshot is simply a
// record in the SnapshotIndex table marking a block height where the full
// PlainState is recoverable: unchanged keys use the current PlainState value,
// changed keys are reconstructed via changesets. The pruner is aware of
// snapshot boundaries and will not delete changesets that are still needed.
type Manager struct {
	cfg    conf.SnapshotConfig
	db     kv.RwDB
	bp     BlockProvider
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	lastSnapshotBlock uint64 // in-memory cache of the latest snapshot height
}

// NewManager creates a snapshot manager. It does not start until Start is called.
func NewManager(cfg conf.SnapshotConfig, db kv.RwDB, bp BlockProvider) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:    cfg,
		db:     db,
		bp:     bp,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

// Start begins the background snapshot creation loop.
func (m *Manager) Start() {
	// Load the latest snapshot height to avoid duplicate work after restart.
	if err := m.db.View(m.ctx, func(tx kv.Tx) error {
		latest, err := LatestSnapshot(tx)
		if err != nil {
			return err
		}
		if latest != nil {
			m.lastSnapshotBlock = latest.BlockNumber
		}
		return nil
	}); err != nil {
		log.Error("Snapshot manager: failed to load latest snapshot", "err", err)
	}

	go m.loop()
	log.Info("Snapshot manager started",
		"interval", m.cfg.Interval,
		"maxRetained", m.cfg.MaxRetained,
		"safetyMargin", m.cfg.SafetyMargin,
	)
}

// Stop signals the manager to stop and waits for it to finish.
func (m *Manager) Stop() {
	m.cancel()
	<-m.done
	log.Info("Snapshot manager stopped")
}

func (m *Manager) loop() {
	defer close(m.done)

	interval := m.cfg.Interval
	if interval == 0 {
		interval = conf.DefaultSnapshotInterval
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.maybeCreate(interval)
		}
	}
}

// maybeCreate checks if a new snapshot should be created and does so.
func (m *Manager) maybeCreate(interval uint64) {
	current := m.bp.CurrentBlock()
	if current == 0 {
		return
	}

	margin := m.cfg.SafetyMargin
	if margin == 0 {
		margin = conf.DefaultSnapshotSafetyMargin
	}
	if current <= margin {
		return
	}
	snapHeight := current - margin

	// Only create when enough blocks have passed since last snapshot.
	if m.lastSnapshotBlock > 0 && snapHeight-m.lastSnapshotBlock < interval {
		return
	}

	// Align to interval boundary for predictable heights.
	snapHeight = (snapHeight / interval) * interval
	if snapHeight == 0 || snapHeight == m.lastSnapshotBlock {
		return
	}

	if err := m.createSnapshot(snapHeight); err != nil {
		log.Error("Snapshot creation failed", "height", snapHeight, "err", err)
		return
	}
	m.lastSnapshotBlock = snapHeight

	// Garbage-collect old snapshots.
	if err := m.gc(); err != nil {
		log.Error("Snapshot GC failed", "err", err)
	}
}

// createSnapshot counts PlainState entries and writes a snapshot index entry.
func (m *Manager) createSnapshot(height uint64) error {
	start := time.Now()

	var meta Meta
	meta.BlockNumber = height
	meta.CreatedAt = time.Now().Unix()

	if err := m.db.View(m.ctx, func(tx kv.Tx) error {
		var err error
		meta.AccountCount, err = countTable(tx, modules.Account)
		if err != nil {
			return err
		}
		meta.StorageCount, err = countTable(tx, modules.Storage)
		if err != nil {
			return err
		}
		meta.CodeCount, err = countTable(tx, modules.Code)
		return err
	}); err != nil {
		return err
	}

	if err := m.db.Update(m.ctx, func(tx kv.RwTx) error {
		return SaveSnapshot(tx, &meta)
	}); err != nil {
		return err
	}

	log.Info("State snapshot created",
		"height", height,
		"accounts", meta.AccountCount,
		"storage", meta.StorageCount,
		"codes", meta.CodeCount,
		"elapsed", time.Since(start).Truncate(time.Millisecond),
	)
	return nil
}

// gc removes old snapshots beyond MaxRetained (keeping the N most recent).
func (m *Manager) gc() error {
	maxRetained := m.cfg.MaxRetained
	if maxRetained <= 0 {
		maxRetained = conf.DefaultSnapshotMaxRetained
	}

	return m.db.Update(m.ctx, func(tx kv.RwTx) error {
		snapshots, err := ListSnapshots(tx)
		if err != nil {
			return err
		}
		// snapshots is sorted ascending by block number.
		excess := len(snapshots) - maxRetained
		for i := 0; i < excess; i++ {
			log.Info("Removing old snapshot", "height", snapshots[i].BlockNumber)
			if err := DeleteSnapshot(tx, snapshots[i].BlockNumber); err != nil {
				return err
			}
		}
		return nil
	})
}

// OldestRetainedBlock returns the block number of the oldest retained snapshot.
// Returns 0 if no snapshots exist. The pruner uses this as the lower bound
// for changeset retention.
func (m *Manager) OldestRetainedBlock() uint64 {
	var block uint64
	_ = m.db.View(m.ctx, func(tx kv.Tx) error {
		oldest, err := OldestSnapshot(tx)
		if err != nil {
			return err
		}
		if oldest != nil {
			block = oldest.BlockNumber
		}
		return nil
	})
	return block
}

// countTable counts entries in a table by scanning keys.
func countTable(tx kv.Tx, table string) (uint64, error) {
	c, err := tx.Cursor(table)
	if err != nil {
		return 0, err
	}
	defer c.Close()

	var count uint64
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}
