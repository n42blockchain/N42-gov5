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

package snapsync

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"

	stderrors "errors"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// Config holds dependencies for the snap sync service.
type Config struct {
	P2P      p2p.P2P
	Chain    common.IBlockChain
	DB       kv.RwDB
	SnapSync *conf.SnapSyncConfig
}

// Service orchestrates the full snap sync lifecycle:
// 1. Wait for minimum peers
// 2. Select pivot block from peer consensus
// 3. Download state via Manager
// 4. Verify downloaded state
// 5. Signal completion so initial-sync can continue from pivot
type Service struct {
	cfg    *Config
	ctx    context.Context
	cancel context.CancelFunc

	synced   atomic.Bool
	syncing  atomic.Bool
	started  sync.Once
	done     chan struct{}
}

// NewService creates a new snap sync service.
func NewService(ctx context.Context, cfg *Config) *Service {
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	return s
}

// Start runs the snap sync process. Blocks until completion or cancellation.
// Safe to call multiple times; only the first call executes.
func (s *Service) Start() {
	s.started.Do(func() {
		defer close(s.done)
		s.run()
	})
}

func (s *Service) run() {
	if !s.cfg.SnapSync.Enable {
		log.Info("Snap sync is disabled, skipping")
		s.synced.Store(true)
		return
	}

	// Check if we actually need snap sync (only for empty/near-genesis nodes).
	currentBlock := s.cfg.Chain.CurrentBlock().Number64().Uint64()
	if currentBlock > 0 {
		log.Info("Node already has state, skipping snap sync", "block", currentBlock)
		s.synced.Store(true)
		return
	}

	// Check if a previous snap sync already completed.
	alreadyDone, err := s.checkPreviousCompletion()
	if err != nil {
		log.Error("Failed to check previous snap sync state", "err", err)
	}
	if alreadyDone {
		log.Info("Previous snap sync already completed")
		s.synced.Store(true)
		return
	}

	s.syncing.Store(true)
	defer func() {
		s.syncing.Store(false)
		s.synced.Store(true)
	}()

	log.Info("Starting snap sync...")

	startTime := time.Now()

	// Try snapshot-based sync first: discover a snapshot from peers,
	// download state at that height, then the caller replays remaining blocks.
	if s.trySnapshotSync() {
		return
	}

	// Fallback to original pivot-based snap sync.
	log.Info("Snapshot sync unavailable, falling back to pivot-based snap sync")

	// Step 1: Wait for peers and select pivot.
	pivotBlock, stateRoot, err := s.selectPivot()
	if err != nil {
		if stderrors.Is(err, ErrSnapSyncNotNeeded) {
			log.Info("Snap sync not needed, falling back to regular sync", "reason", err)
			return
		}
		log.Error("Snap sync failed to select pivot", "err", err)
		return
	}

	log.Info("Snap sync pivot selected",
		"pivotBlock", pivotBlock,
		"stateRoot", fmt.Sprintf("%x", stateRoot),
	)

	// Step 2: Save pivot and run download manager.
	if err := s.cfg.DB.Update(s.ctx, func(tx kv.RwTx) error {
		return SavePivotBlock(tx, pivotBlock)
	}); err != nil {
		log.Error("Failed to save pivot block", "err", err)
		return
	}

	mgr := NewManager(s.cfg.SnapSync, s.cfg.P2P, s.cfg.DB, pivotBlock, stateRoot)

	if err := mgr.Run(s.ctx); err != nil {
		log.Error("Snap sync download failed", "err", err)
		return
	}

	s.verifyAndLog(startTime)
}

// Stop cancels the snap sync service.
func (s *Service) Stop() error {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(30 * time.Second):
		log.Warn("Timed out waiting for snap sync to stop")
	}
	return nil
}

// Syncing returns true if snap sync is actively running.
func (s *Service) Syncing() bool {
	return s.syncing.Load()
}

// Synced returns true if snap sync has completed (or was skipped).
func (s *Service) Synced() bool {
	return s.synced.Load()
}

// Status returns an error if syncing, nil if synced.
func (s *Service) Status() error {
	if s.syncing.Load() {
		return errors.New("snap syncing")
	}
	return nil
}

// Resync is a no-op for snap sync (it only runs once).
func (s *Service) Resync() error {
	return nil
}

// ErrSnapSyncNotNeeded is returned when the network head is below the
// SyncThreshold, meaning regular block-by-block sync is more efficient.
var ErrSnapSyncNotNeeded = stderrors.New("snap sync not needed")

// selectPivot waits for peers and selects a pivot block.
// The pivot block is chosen as (highest_peer_block - PivotDistance) to ensure
// enough peers have the state at that point.
func (s *Service) selectPivot() (uint64, []byte, error) {
	required := s.cfg.SnapSync.MinSnapPeers
	if required <= 0 {
		required = 3
	}

	log.Info("Waiting for snap sync peers...", "required", required)

	var highestBlock *uint256.Int
	var peers []peer.ID

	for {
		currentBlock := s.cfg.Chain.CurrentBlock().Number64()
		highestBlock, peers = s.cfg.P2P.Peers().BestPeers(required, currentBlock)

		if len(peers) >= required && highestBlock.Uint64() > s.cfg.SnapSync.PivotDistance {
			break
		}

		select {
		case <-time.After(5 * time.Second):
		case <-s.ctx.Done():
			return 0, nil, s.ctx.Err()
		}
	}

	highest := highestBlock.Uint64()

	// If the network is not far enough ahead, regular sync is more efficient.
	threshold := s.cfg.SnapSync.SyncThreshold
	if threshold > 0 && highest < threshold {
		return 0, nil, fmt.Errorf("%w: network head %d below threshold %d",
			ErrSnapSyncNotNeeded, highest, threshold)
	}

	if highest <= s.cfg.SnapSync.PivotDistance {
		return 0, nil, fmt.Errorf("network head %d too low for pivot distance %d", highest, s.cfg.SnapSync.PivotDistance)
	}
	pivotBlock := highest - s.cfg.SnapSync.PivotDistance

	// For N42's incremental state root, we pass a nil state root.
	// State verification relies on block replay, not state root comparison.
	return pivotBlock, nil, nil
}

// trySnapshotSync attempts snapshot-based sync. Returns true if successful.
func (s *Service) trySnapshotSync() bool {
	sm := NewSnapshotManager(s.cfg.SnapSync, s.cfg.P2P, s.cfg.DB)

	// Wait up to 30 seconds for enough peers with snapshots.
	var snapshotBlock uint64
	deadline := time.After(30 * time.Second)
	minPeers := s.cfg.SnapSync.MinSnapPeers
	if minPeers <= 0 {
		minPeers = 2
	}

	for {
		var err error
		snapshotBlock, err = sm.DiscoverSnapshot(s.ctx, minPeers)
		if err == nil && snapshotBlock > 0 {
			break
		}

		select {
		case <-time.After(5 * time.Second):
			log.Debug("Waiting for snapshot peers...", "err", err)
		case <-deadline:
			log.Info("No snapshots discovered from peers, using fallback sync")
			return false
		case <-s.ctx.Done():
			return false
		}
	}

	log.Info("Starting snapshot-based sync", "snapshotBlock", snapshotBlock)
	startTime := time.Now()

	if err := sm.Run(s.ctx, snapshotBlock); err != nil {
		log.Error("Snapshot sync download failed", "err", err)
		return false
	}

	s.verifyAndLog(startTime)
	return true
}

// verifyAndLog runs post-download verification and logs completion.
func (s *Service) verifyAndLog(startTime time.Time) {
	stats, err := VerifyDownloadedState(s.ctx, s.cfg.DB)
	if err != nil {
		log.Error("Snap sync state verification failed", "err", err)
		return
	}

	if err := VerifyAccountIntegrity(s.ctx, s.cfg.DB, 1000); err != nil {
		log.Error("Snap sync account integrity check failed", "err", err)
		return
	}

	log.Info("Snap sync completed successfully",
		"duration", time.Since(startTime),
		"accounts", stats.AccountCount,
		"storageSlots", stats.StorageCount,
		"codes", stats.CodeCount,
	)
}

// checkPreviousCompletion checks if a previous snap sync run already completed.
func (s *Service) checkPreviousCompletion() (bool, error) {
	var state string
	if err := s.cfg.DB.View(s.ctx, func(tx kv.Tx) error {
		var err error
		state, err = LoadState(tx)
		return err
	}); err != nil {
		return false, err
	}
	return state == StateCompleted, nil
}
