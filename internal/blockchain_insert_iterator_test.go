// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers insertIterator (blockchain_insert.go): next/peek/previous/current/
// first/remaining/processed walking a 3-block chain, including a
// header-verification error short-circuiting ValidateBody and a body
// validation error on an otherwise header-valid block.

package internal

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/modules/state"
)

// fakeValidator implements the Validator interface with a per-call toggle so
// tests can make ValidateBody fail for a specific block index.
type fakeValidator struct {
	failBodyAtHash map[block.IBlock]bool
}

func (v *fakeValidator) ValidateBody(b block.IBlock) error {
	if v.failBodyAtHash != nil && v.failBodyAtHash[b] {
		return errors.New("bad body")
	}
	return nil
}

func (v *fakeValidator) ValidateState(block.IBlock, *state.IntraBlockState, block.Receipts, uint64) error {
	return nil
}

func mkInsertBlock(number uint64) block.IBlock {
	return testConcreteBlock(&block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(1),
	}, &block.Body{})
}

func TestInsertIteratorWalksChainAndTracksPosition(t *testing.T) {
	chain := []block.IBlock{mkInsertBlock(1), mkInsertBlock(2), mkInsertBlock(3)}
	results := make(chan error, 3)
	results <- nil
	results <- nil
	results <- nil
	close(results)

	it := newInsertIterator(chain, results, &fakeValidator{})

	if it.first().Number64().Uint64() != 1 {
		t.Fatalf("first() = %v, want block 1", it.first())
	}
	if it.current() != nil {
		t.Fatalf("current() before any next() = %v, want nil", it.current())
	}
	if it.previous() != nil {
		t.Fatalf("previous() before any next() = %v, want nil", it.previous())
	}
	if it.processed() != 0 {
		t.Fatalf("processed() before any next() = %d, want 0", it.processed())
	}

	peeked, err := it.peek()
	if err != nil || peeked.Number64().Uint64() != 1 {
		t.Fatalf("peek() = (%v, %v), want block 1", peeked, err)
	}

	blk, err := it.next()
	if err != nil || blk.Number64().Uint64() != 1 {
		t.Fatalf("next() #1 = (%v, %v), want block 1", blk, err)
	}
	if it.current().Number64().Uint64() != 1 {
		t.Fatalf("current() after next() #1 = %v, want block 1", it.current())
	}
	if it.processed() != 1 {
		t.Fatalf("processed() after next() #1 = %d, want 1", it.processed())
	}
	if it.remaining() != 3 {
		t.Fatalf("remaining() after next() #1 = %d, want 3 (len(chain) - index)", it.remaining())
	}

	blk2, err := it.next()
	if err != nil || blk2.Number64().Uint64() != 2 {
		t.Fatalf("next() #2 = (%v, %v), want block 2", blk2, err)
	}
	if it.previous().Number64().Uint64() != 1 {
		t.Fatalf("previous() after next() #2 = %v, want block 1", it.previous())
	}

	blk3, err := it.next()
	if err != nil || blk3.Number64().Uint64() != 3 {
		t.Fatalf("next() #3 = (%v, %v), want block 3", blk3, err)
	}

	done, err := it.next()
	if done != nil || err != nil {
		t.Fatalf("next() past end = (%v, %v), want (nil, nil)", done, err)
	}
}

func TestInsertIteratorHeaderErrorShortCircuitsBody(t *testing.T) {
	chain := []block.IBlock{mkInsertBlock(1)}
	headerErr := errors.New("bad header")
	results := make(chan error, 1)
	results <- headerErr
	close(results)

	v := &fakeValidator{failBodyAtHash: map[block.IBlock]bool{}}
	it := newInsertIterator(chain, results, v)

	blk, err := it.next()
	if err != headerErr {
		t.Fatalf("next() error = %v, want %v", err, headerErr)
	}
	if blk == nil {
		t.Fatalf("next() block = nil even on header error, want the block")
	}
}

func TestInsertIteratorBodyValidationError(t *testing.T) {
	bad := mkInsertBlock(1)
	chain := []block.IBlock{bad}
	results := make(chan error, 1)
	results <- nil
	close(results)

	v := &fakeValidator{failBodyAtHash: map[block.IBlock]bool{bad: true}}
	it := newInsertIterator(chain, results, v)

	_, err := it.next()
	if err == nil {
		t.Fatalf("next() with bad body = nil error, want an error")
	}
}
