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
	var (
		imported int
		err      error
	)
	if authorized {
		imported, err = s.insertAuthorized(blocks)
	} else {
		imported, err = s.cfg.chain.InsertChain(blocks)
	}
	if err != nil && errors.Is(err, consensus.ErrExecutionInvalid) && imported >= 0 && imported < len(blocks) {
		s.setBadBlock(ctx, blocks[imported].Hash())
	}
	return blocks, imported, err
}
