// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"errors"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
)

type catchUpTestChain struct {
	common.IBlockChain
	err         error
	failedIndex int
	insertCalls int
}

func (c *catchUpTestChain) InsertChain([]block.IBlock) (int, error) {
	c.insertCalls++
	return c.failedIndex, c.err
}

func newBadBlockTestService(t *testing.T, chain common.IBlockChain) *Service {
	t.Helper()
	cache, err := lru.New[types.Hash, bool](badBlockSize)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		ctx:           context.Background(),
		cfg:           &config{chain: chain},
		badBlockCache: cache,
	}
}

func testBlock(number uint64, parent types.Hash) block.IBlock {
	return block.NewBlock(&block.Header{
		ParentHash: parent,
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(0),
	}, nil)
}

func TestCatchUpCachesExecutionInvalidAndSkipsNextInsert(t *testing.T) {
	chain := &catchUpTestChain{err: fmtExecutionInvalid("nonce too low"), failedIndex: 0}
	svc := newBadBlockTestService(t, chain)
	blk := testBlock(1, types.Hash{1})

	filtered, _, err := svc.insertCatchUpBlocks(context.Background(), []block.IBlock{blk}, false)
	if !errors.Is(err, consensus.ErrExecutionInvalid) {
		t.Fatalf("first insert error = %v, want ErrExecutionInvalid", err)
	}
	if len(filtered) != 1 || !svc.hasBadBlock(blk.Hash()) {
		t.Fatal("execution-invalid block was not cached")
	}

	filtered, _, err = svc.insertCatchUpBlocks(context.Background(), []block.IBlock{blk}, false)
	if err != nil || len(filtered) != 0 {
		t.Fatalf("second insert = (%d blocks, %v), want cached block dropped", len(filtered), err)
	}
	if chain.insertCalls != 1 {
		t.Fatalf("InsertChain called %d times, want 1", chain.insertCalls)
	}
}

func TestCatchUpDoesNotCacheTransientInsertErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "unknown ancestor", err: consensus.ErrUnknownAncestor},
		{name: "revert unavailable", err: errors.New("branch-switch revert unavailable (recoverable)")},
		{name: "sibling parent", err: errors.New("sibling parent not applied")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain := &catchUpTestChain{err: tt.err, failedIndex: 0}
			svc := newBadBlockTestService(t, chain)
			blk := testBlock(1, types.Hash{2})

			_, _, _ = svc.insertCatchUpBlocks(context.Background(), []block.IBlock{blk}, false)
			if svc.hasBadBlock(blk.Hash()) {
				t.Fatalf("transient error %q cached block as bad", tt.err)
			}
		})
	}
}

func TestFilterBadBlocksPropagatesToDescendants(t *testing.T) {
	svc := newBadBlockTestService(t, &catchUpTestChain{})
	bad := testBlock(1, types.Hash{3})
	child := testBlock(2, bad.Hash())
	grandchild := testBlock(3, child.Hash())
	svc.setBadBlock(context.Background(), bad.Hash())

	filtered := svc.filterBadBlocks(context.Background(), []block.IBlock{bad, child, grandchild})
	if len(filtered) != 0 {
		t.Fatalf("filtered length = %d, want 0", len(filtered))
	}
	if !svc.hasBadBlock(child.Hash()) || !svc.hasBadBlock(grandchild.Hash()) {
		t.Fatal("bad ancestry was not propagated across fetched range")
	}
}

func fmtExecutionInvalid(detail string) error {
	return errors.Join(consensus.ErrExecutionInvalid, errors.New(detail))
}
