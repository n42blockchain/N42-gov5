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

// Package backfill implements background history backfilling after checkpoint sync.
//
// When a node starts via checkpoint/snap sync, it only has recent blocks.
// The backfill service downloads historical blocks from peers in reverse
// order (from the checkpoint toward genesis) so the node eventually has
// a complete history without blocking initial availability.
//
// This is the N42 equivalent of reth's BackfillSync: the node is immediately
// usable after checkpoint sync while history is filled in the background.
package backfill

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/p2p"
	n42sync "github.com/n42blockchain/N42/internal/sync"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/common/utils"
)

// Config controls backfill behavior.
type Config struct {
	// Enabled activates background backfilling. Default: false.
	Enabled bool
	// BatchSize is the number of blocks to request per P2P call. Default: 64.
	BatchSize uint64
	// Interval is the pause between backfill batches. Default: 5s.
	Interval time.Duration
}

// DefaultConfig returns sensible defaults with backfill disabled.
func DefaultConfig() Config {
	return Config{
		Enabled:   false,
		BatchSize: 64,
		Interval:  5 * time.Second,
	}
}

// Service runs background history backfilling.
type Service struct {
	db    kv.RwDB
	p2p   p2p.P2P
	chain common.IBlockChain
	cfg   Config

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	// backfillHead is the lowest block we have already backfilled to.
	// We work downward from the checkpoint toward genesis.
	backfillHead atomic.Uint64
	// totalFilled counts blocks backfilled this session.
	totalFilled atomic.Uint64
}

// New creates a new backfill service.
func New(db kv.RwDB, p2p p2p.P2P, chain common.IBlockChain, cfg Config) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		db:    db,
		p2p:   p2p,
		chain: chain,
		cfg:   cfg,
		ctx:   ctx,
		cancel: cancel,
		done:  make(chan struct{}),
	}
}

// Start begins background backfilling. Call after checkpoint sync completes.
// startBlock is the checkpoint block number (lowest block we already have).
func (s *Service) Start(startBlock uint64) {
	if !s.cfg.Enabled || startBlock == 0 {
		close(s.done)
		return
	}
	s.backfillHead.Store(startBlock)

	go func() {
		defer close(s.done)
		s.run()
	}()

	log.Info("Backfill service started",
		"from", startBlock,
		"batchSize", s.cfg.BatchSize,
		"interval", s.cfg.Interval,
	)
}

// Stop gracefully shuts down the backfill service.
func (s *Service) Stop() {
	s.cancel()
	<-s.done
	log.Info("Backfill service stopped",
		"lowestBlock", s.backfillHead.Load(),
		"totalFilled", s.totalFilled.Load(),
	)
}

// Progress returns the current backfill progress.
func (s *Service) Progress() (lowestBlock uint64, totalFilled uint64) {
	return s.backfillHead.Load(), s.totalFilled.Load()
}

// IsComplete returns true if backfill has reached genesis.
func (s *Service) IsComplete() bool {
	return s.backfillHead.Load() == 0
}

func (s *Service) run() {
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if s.backfillHead.Load() == 0 {
				log.Info("Backfill complete — all historical blocks downloaded")
				return
			}
			s.fillBatch()
		}
	}
}

func (s *Service) fillBatch() {
	head := s.backfillHead.Load()
	if head == 0 {
		return
	}

	// Calculate range to download: [start, head-1] going backward
	batchSize := s.cfg.BatchSize
	var start uint64
	if head > batchSize {
		start = head - batchSize
	} else {
		start = 0
		batchSize = head
	}

	if batchSize == 0 {
		s.backfillHead.Store(0)
		return
	}

	// Get connected peers
	peers := s.p2p.Peers().Connected()
	if len(peers) == 0 {
		return // no peers, try again later
	}

	// Download blocks from a peer
	blocks, err := s.downloadRange(start, batchSize, peers)
	if err != nil {
		log.Debug("Backfill batch download failed", "start", start, "count", batchSize, "err", err)
		return
	}

	if len(blocks) == 0 {
		return
	}

	// Write blocks to database
	written, err := s.writeBlocks(blocks)
	if err != nil {
		log.Error("Backfill write failed", "err", err)
		return
	}

	s.totalFilled.Add(uint64(written))

	// Update backfill head to lowest written block
	newHead := start
	s.backfillHead.Store(newHead)

	log.Debug("Backfill batch complete",
		"range", [2]uint64{start, start + uint64(written) - 1},
		"written", written,
		"remaining", newHead,
		"totalFilled", s.totalFilled.Load(),
	)
}

func (s *Service) downloadRange(start, count uint64, peers []peer.ID) ([]*block.Block, error) {
	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(start)),
		Count:            count,
		Step:             1,
	}

	for _, pid := range peers {
		select {
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		default:
		}

		blocks, err := n42sync.SendBodiesByRangeRequest(s.ctx, s.chain, s.p2p, pid, req, nil)
		if err != nil || len(blocks) == 0 {
			continue
		}
		// SendBodiesByRangeRequest already RLP-decodes to *block.Block.
		return blocks, nil
	}

	return nil, nil
}

func (s *Service) writeBlocks(blocks []*block.Block) (int, error) {
	tx, err := s.db.BeginRw(s.ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	written := 0
	for _, blk := range blocks {
		num := blk.Number64()
		if num == nil {
			continue
		}
		blockNum := num.Uint64()
		hash := blk.Hash()

		// Skip only when BOTH header and body are present (either hot or
		// served by the era store). Header-only skipping would foreclose
		// the sole self-heal path for a sealed range whose exec era is
		// damaged/pruned: the chain era answers HasHeader while the body
		// is genuinely gone.
		if rawdb.HasHeader(tx, hash, blockNum) && rawdb.HasBlock(tx, hash, blockNum) {
			continue
		}

		// Write header, body, and hash mapping
		hdr, ok := blk.Header().(*block.Header)
		if !ok {
			continue
		}
		body, ok := blk.Body().(*block.Body)
		if !ok {
			continue
		}
		rawdb.WriteHeader(tx, hdr)
		if err := rawdb.WriteBody(tx, hash, blockNum, body); err != nil {
			return written, err
		}
		rawdb.WriteCanonicalHash(tx, hash, blockNum)
		written++
	}

	if written > 0 {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	}

	return written, nil
}
