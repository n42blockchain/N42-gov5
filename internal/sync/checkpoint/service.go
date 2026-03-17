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

package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/api/protocol/sync_pb"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
	n42sync "github.com/n42blockchain/N42/internal/sync"
	"github.com/n42blockchain/N42/internal/sync/snapsync"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/utils"
)

var (
	// ErrHashMismatch is returned when the downloaded block's hash does not match
	// the trusted checkpoint hash.
	ErrHashMismatch = errors.New("checkpoint block hash mismatch")

	// ErrNoPeers is returned when no peers are available to download from.
	ErrNoPeers = errors.New("no peers available for checkpoint block download")

	// ErrBlockNotReceived is returned when peers did not return the requested block.
	ErrBlockNotReceived = errors.New("checkpoint block not received from peers")

	// peerWaitTimeout is how long we wait for peers before giving up.
	peerWaitTimeout = 2 * time.Minute

	// maxDownloadRetries is the number of times to retry downloading from different peers.
	maxDownloadRetries = 5
)

// Config holds dependencies for the checkpoint sync service.
type Config struct {
	Checkpoint *conf.CheckpointConfig
	P2P        p2p.P2P
	Chain      common.IBlockChain
	DB         kv.RwDB
}

// Service validates and downloads a trusted checkpoint block from peers.
// Once validated, it stores the block in the DB and sets it as the snap sync
// pivot block, allowing the node to skip syncing from genesis.
type Service struct {
	cfg *Config
}

// NewService creates a new checkpoint sync service.
func NewService(cfg *Config) *Service {
	return &Service{cfg: cfg}
}

// Start validates the checkpoint configuration and downloads the trusted block.
// It returns the checkpoint block number on success so that snap sync can use it
// as the pivot block. If checkpoint sync is disabled or the block is already in
// the DB, it returns 0 with no error.
func (s *Service) Start(ctx context.Context) (uint64, error) {
	if s.cfg.Checkpoint == nil || !s.cfg.Checkpoint.IsEnabled() {
		return 0, nil
	}

	if err := s.cfg.Checkpoint.Validate(); err != nil {
		return 0, fmt.Errorf("checkpoint config validation failed: %w", err)
	}

	blockNum := s.cfg.Checkpoint.BlockNumber
	expectedHash := types.HexToHash(s.cfg.Checkpoint.BlockHash)

	log.Info("Checkpoint sync enabled",
		"blockNumber", blockNum,
		"expectedHash", expectedHash.String(),
	)

	// Check if we already have this block in the DB.
	alreadyHave, err := s.checkExistingBlock(ctx, blockNum, expectedHash)
	if err != nil {
		return 0, fmt.Errorf("failed to check existing checkpoint block: %w", err)
	}
	if alreadyHave {
		log.Info("Checkpoint block already exists in DB, skipping download", "block", blockNum)
		return s.setPivot(ctx, blockNum)
	}

	// Download the block from peers.
	blk, err := s.downloadBlock(ctx, blockNum)
	if err != nil {
		return 0, fmt.Errorf("failed to download checkpoint block %d: %w", blockNum, err)
	}

	// Verify the block hash matches the trusted checkpoint.
	actualHash := blk.Hash()
	if actualHash != expectedHash {
		return 0, fmt.Errorf("%w: expected %s, got %s at block %d",
			ErrHashMismatch, expectedHash.String(), actualHash.String(), blockNum)
	}

	log.Info("Checkpoint block hash verified", "block", blockNum, "hash", actualHash.String())

	// Store the block in the DB.
	if err := s.storeBlock(ctx, blk, blockNum); err != nil {
		return 0, fmt.Errorf("failed to store checkpoint block: %w", err)
	}

	// Set this as the snap sync pivot block.
	return s.setPivot(ctx, blockNum)
}

// checkExistingBlock returns true if the block at the given number already exists
// in the DB and its hash matches the expected hash.
func (s *Service) checkExistingBlock(ctx context.Context, blockNum uint64, expectedHash types.Hash) (bool, error) {
	var found bool
	err := s.cfg.DB.View(ctx, func(tx kv.Tx) error {
		hash, err := rawdb.ReadCanonicalHash(tx, blockNum)
		if err != nil {
			return err
		}
		if hash == (types.Hash{}) {
			return nil
		}
		if hash != expectedHash {
			// Block exists but with different hash — this is a serious issue.
			return fmt.Errorf("existing block %d has hash %s, expected checkpoint hash %s",
				blockNum, hash.String(), expectedHash.String())
		}

		headerNumber := rawdb.ReadHeaderNumber(tx, hash)
		if headerNumber == nil || *headerNumber != blockNum || rawdb.ReadBlock(tx, hash, blockNum) == nil {
			log.Warn("Checkpoint block mapping exists without complete block data, redownloading",
				"block", blockNum, "hash", hash.String())
			return nil
		}

		// Block exists and is complete.
		found = true
		return nil
	})
	return found, err
}

// downloadBlock requests a single block from peers via the P2P BodiesByRange RPC.
// It waits for peers to be available, then tries multiple peers until successful.
func (s *Service) downloadBlock(ctx context.Context, blockNum uint64) (*block.Block, error) {
	log.Info("Downloading checkpoint block from peers...", "block", blockNum)

	// Wait for at least one connected peer.
	peers, err := s.waitForPeers(ctx)
	if err != nil {
		return nil, err
	}

	// Try downloading from available peers.
	startNum := uint256.NewInt(blockNum)
	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(startNum),
		Count:            1,
		Step:             1,
	}

	var lastErr error
	for attempt := 0; attempt < maxDownloadRetries; attempt++ {
		for _, pid := range peers {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			pbBlocks, err := n42sync.SendBodiesByRangeRequest(ctx, s.cfg.Chain, s.cfg.P2P, pid, req, nil)
			if err != nil {
				log.Debug("Failed to download checkpoint block from peer",
					"peer", pid.String(), "err", err)
				lastErr = err
				continue
			}

			if len(pbBlocks) == 0 {
				log.Debug("Peer returned no blocks for checkpoint request",
					"peer", pid.String(), "block", blockNum)
				lastErr = ErrBlockNotReceived
				continue
			}

			// Convert protobuf block to native block type.
			blk := new(block.Block)
			if err := blk.FromProtoMessage(pbBlocks[0]); err != nil {
				log.Debug("Failed to decode checkpoint block from peer",
					"peer", pid.String(), "err", err)
				lastErr = fmt.Errorf("decode block: %w", err)
				continue
			}

			log.Info("Checkpoint block downloaded successfully",
				"block", blockNum, "peer", pid.String())
			return blk, nil
		}

		// Refresh peer list between retry rounds.
		if attempt < maxDownloadRetries-1 {
			log.Debug("Retrying checkpoint block download with refreshed peers",
				"attempt", attempt+1)
			time.Sleep(2 * time.Second)
			peers = s.cfg.P2P.Peers().Connected()
			if len(peers) == 0 {
				return nil, ErrNoPeers
			}
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all download attempts failed, last error: %w", lastErr)
	}
	return nil, ErrBlockNotReceived
}

// waitForPeers blocks until at least one peer is connected, or the timeout expires.
func (s *Service) waitForPeers(ctx context.Context) ([]peer.ID, error) {
	deadline := time.NewTimer(peerWaitTimeout)
	defer deadline.Stop()

	poll := time.NewTimer(0) // fire immediately on first iteration
	defer poll.Stop()

	for {
		peers := s.cfg.P2P.Peers().Connected()
		if len(peers) > 0 {
			log.Info("Peers available for checkpoint download", "count", len(peers))
			return peers, nil
		}

		select {
		case <-poll.C:
			log.Debug("Waiting for peers to download checkpoint block...")
			poll.Reset(3 * time.Second)
		case <-deadline.C:
			return nil, ErrNoPeers
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// storeBlock writes the checkpoint block (header + body) to the database
// and sets the canonical hash mapping.
func (s *Service) storeBlock(ctx context.Context, blk *block.Block, blockNum uint64) error {
	return s.cfg.DB.Update(ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, blk); err != nil {
			return fmt.Errorf("write block: %w", err)
		}
		if err := rawdb.WriteCanonicalHash(tx, blk.Hash(), blockNum); err != nil {
			return fmt.Errorf("write canonical hash: %w", err)
		}
		log.Info("Checkpoint block stored in DB", "block", blockNum, "hash", blk.Hash().String())
		return nil
	})
}

// setPivot writes the checkpoint block number as the snap sync pivot block.
func (s *Service) setPivot(ctx context.Context, blockNum uint64) (uint64, error) {
	err := s.cfg.DB.Update(ctx, func(tx kv.RwTx) error {
		return snapsync.SavePivotBlock(tx, blockNum)
	})
	if err != nil {
		return 0, fmt.Errorf("failed to save pivot block: %w", err)
	}
	log.Info("Checkpoint set as snap sync pivot", "pivotBlock", blockNum)
	return blockNum, nil
}
