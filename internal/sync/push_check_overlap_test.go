// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// overlapTestChain is a lag-owned chain that queues pushed blocks
// unchecked (N42_PUSH_CHECK_OVERLAP) and records the order of the calls.
type overlapTestChain struct {
	*lagOwnedTestChain
	unchecked bool
	calls     []string
}

func (c *overlapTestChain) note(s string) {
	c.mu.Lock()
	c.calls = append(c.calls, s)
	c.mu.Unlock()
}

func (c *overlapTestChain) LagEnqueueUnchecked(*block.Block) bool {
	c.note("enqueue")
	return c.unchecked
}

func (c *overlapTestChain) CheckDeferredBlock(blk block.IBlock) (bool, bool, error) {
	c.note("check")
	return c.lagOwnedTestChain.CheckDeferredBlock(blk)
}

func (c *overlapTestChain) LagDropUnchecked(h types.Hash) bool {
	c.note("drop")
	_, queued := c.queued.Load(h)
	return !queued
}

func (c *overlapTestChain) InsertChain(b []block.IBlock) (int, error) {
	c.note("insert")
	return c.orderTestChain.InsertChain(b)
}

// TestHandlePushedBlockCheckOverlap: with the overlap the push handler
// queues a lag-owned block before its check and offers it for dropping
// after; a passing check leaves the import to the executor, a failing one
// takes the path it takes without the overlap (InsertChain, which fails
// the bad block) and no import notification fires; without the overlap
// nothing is dropped.
func TestHandlePushedBlockCheckOverlap(t *testing.T) {
	for _, tc := range []struct {
		name      string
		unchecked bool
		checkErr  error
		want      []string
	}{
		{"pass", true, nil, []string{"enqueue", "check", "drop"}},
		{"fail", true, errors.New("availability: bad"), []string{"enqueue", "check", "drop", "insert"}},
		{"off", false, nil, []string{"enqueue", "check"}},
		{"offFail", false, errors.New("availability: bad"), []string{"enqueue", "check", "insert"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := &overlapTestChain{lagOwnedTestChain: &lagOwnedTestChain{orderTestChain: newOrderTestChain()}, unchecked: tc.unchecked}
			chain.checkErr = tc.checkErr
			if tc.checkErr != nil {
				chain.insertErr = tc.checkErr
			}
			notifier := &recordingNotifier{}
			svc := &Service{ctx: context.Background(), cfg: &config{chain: chain, blockImportNotifier: notifier}}
			blk := orderTestBlock(4, types.Hash{0x04})
			svc.handlePushedBlock(blk, false)
			select {
			case <-chain.checkDoneCh:
			case <-time.After(2 * time.Second):
				t.Fatal("no check")
			}
			chain.mu.Lock()
			calls := append([]string(nil), chain.calls...)
			chain.mu.Unlock()
			if len(calls) != len(tc.want) {
				t.Fatalf("calls %v, want %v", calls, tc.want)
			}
			for i := range calls {
				if calls[i] != tc.want[i] {
					t.Fatalf("calls %v, want %v", calls, tc.want)
				}
			}
			notifier.mu.Lock()
			imported, checked := notifier.imported, len(notifier.checked)
			notifier.mu.Unlock()
			if imported != 0 {
				t.Fatalf("import notified %d times", imported)
			}
			if wantChecked := tc.checkErr == nil; (checked != 0) != wantChecked {
				t.Fatalf("check notified %d times, want passed=%v", checked, wantChecked)
			}
		})
	}
}
