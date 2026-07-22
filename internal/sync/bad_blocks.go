// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus"
)

// rejectBadBlock reports whether a block or its parent is already known bad.
// Parent-to-child propagation makes a fetched range collapse in one pass.
func (s *Service) rejectBadBlock(ctx context.Context, blk block.IBlock) bool {
	if blk == nil {
		return true
	}
	if s.hasBadBlock(blk.Hash()) {
		return true
	}
	if s.hasBadBlock(blk.ParentHash()) {
		s.setBadBlock(ctx, blk.Hash())
		return true
	}
	return false
}

func (s *Service) filterBadBlocks(ctx context.Context, blocks []block.IBlock) []block.IBlock {
	filtered := make([]block.IBlock, 0, len(blocks))
	for _, blk := range blocks {
		if !s.rejectBadBlock(ctx, blk) {
			filtered = append(filtered, blk)
		}
	}
	return filtered
}

// insertCatchUpBlocks filters cached bad branches before insertion and records
// only deterministic execution failures. The returned index identifies the
// first unimported block in the filtered slice.
func (s *Service) insertCatchUpBlocks(ctx context.Context, blocks []block.IBlock, authorized bool) ([]block.IBlock, int, error) {
	blocks = s.filterBadBlocks(ctx, blocks)
	if len(blocks) == 0 {
		return blocks, 0, nil
	}
	// Insert one block at a time, driving the consensus engine via
	// NotifyBlockImported after each. A BULK insert verifies every header up-front
	// against the engine's CURRENT validator set, so a range that crosses an
	// epoch-boundary validator-set change (reconfiguration) fails to verify the
	// post-boundary blocks — their QC carries a signer bitmap sized to the NEW set
	// while the engine still holds the OLD one, and the batch never advances the
	// engine's epoch mid-way. Advancing the view/epoch and applying the staged
	// reconfiguration after each block, before the next block's header is verified,
	// mirrors the live push path and lets a lagging node catch up ACROSS
	// reconfigurations. Verify/execute of a single sequential block is equivalent
	// to the batch; only the ordering relative to engine advancement changes.
	notifier := s.cfg.blockImportNotifier
	total := 0
	for i := range blocks {
		var (
			imported int
			err      error
		)
		one := blocks[i : i+1]
		if authorized {
			imported, err = s.insertAuthorized(one)
		} else {
			imported, err = s.cfg.chain.InsertChain(one)
		}
		if imported > 0 {
			total++
			if notifier != nil {
				// Applies any epoch-boundary reconfig this block crosses, so the
				// NEXT block's header verifies against the correct set.
				notifier.NotifyBlockImported(blocks[i].Hash(), blocks[i].TxHash())
			}
		}
		if err != nil {
			if errors.Is(err, consensus.ErrExecutionInvalid) {
				s.setBadBlock(ctx, blocks[i].Hash())
			}
			return blocks, total, err
		}
	}
	return blocks, total, nil
}
