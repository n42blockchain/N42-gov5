// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Catch-up range windows and one-window prefetch.
//
// A from-genesis catch-up used to request up to 1024 blocks (in practice
// the server's 64 MiB byte budget, ~480 blocks of 140 KB) and import them
// only once the whole response was decoded: the fetch (~1.3 s a response)
// sat in series with execution, and the decoded response (~0.7 MB a block
// of transaction objects) set the heap peak. Requests are now windows of
// catchUpWindowBlocks, and while one window executes the next is fetched
// from the same peer, so at most two windows are held and the transfer
// overlaps execution. The wire is unchanged (the same bodies_by_range
// request, only a smaller count); every response still goes through the
// same screening, anchor gate and import as before.

package sync

import (
	"context"
	gosync "sync"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// defaultCatchUpWindowBlocks caps the blocks of one catch-up range request.
// At ~140 KB / ~1,200 transactions a block a window decodes to ~90 MB and
// imports in ~2 s, against ~0.3 s to transfer it.
const defaultCatchUpWindowBlocks = 128

var catchUpWindow = envUint("N42_CATCHUP_WINDOW_BLOCKS", defaultCatchUpWindowBlocks, 1, maxRequestBlocks)

func catchUpWindowBlocks() uint64 { return catchUpWindow }

// setCatchUpWindowForTest overrides the window and returns a restore function.
func setCatchUpWindowForTest(n uint64) (restore func()) {
	prev := catchUpWindow
	catchUpWindow = n
	return func() { catchUpWindow = prev }
}

// rangePrefetch is one window requested ahead of the import.
type rangePrefetch struct {
	pid    peer.ID
	start  uint64
	count  uint64
	cancel context.CancelFunc
	done   chan struct{}
	blocks []*block.Block
	err    error
}

type catchUpPrefetcher struct {
	mu  gosync.Mutex
	cur *rangePrefetch
}

// prefetchCount is the count catchUpRound will ask for at start: the window,
// bounded by target.
func prefetchCount(start, target uint64) uint64 {
	if start > target {
		return 0
	}
	count := target - start + 1
	if w := catchUpWindowBlocks(); count > w {
		count = w
	}
	return count
}

// startCatchUpPrefetch requests the window at start (up to target) from pid
// in the background, replacing any prefetch still pending.
func (s *Service) startCatchUpPrefetch(pid peer.ID, start, target uint64) {
	count := prefetchCount(start, target)
	if count == 0 || s.ctx == nil {
		return
	}
	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(start)),
		Count:            count,
		Step:             1,
	}
	timeout, _ := s.catchUpPeerRequest(pid, req, start+count-1)
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	p := &rangePrefetch{pid: pid, start: start, count: count, cancel: cancel, done: make(chan struct{})}
	s.catchUpPrefetch.mu.Lock()
	old := s.catchUpPrefetch.cur
	s.catchUpPrefetch.cur = p
	s.catchUpPrefetch.mu.Unlock()
	if old != nil {
		old.cancel()
	}
	go func() {
		defer close(p.done)
		defer cancel()
		p.blocks, p.err = SendBodiesByRangeRequest(ctx, s.cfg.chain, s.cfg.p2p, pid, req, nil)
	}()
}

// takeCatchUpPrefetch removes the pending prefetch and returns it when it is
// exactly req to pid; any other pending prefetch is cancelled.
func (s *Service) takeCatchUpPrefetch(pid peer.ID, req *sync_pb.BodiesByRangeRequest) *rangePrefetch {
	s.catchUpPrefetch.mu.Lock()
	p := s.catchUpPrefetch.cur
	s.catchUpPrefetch.cur = nil
	s.catchUpPrefetch.mu.Unlock()
	if p == nil {
		return nil
	}
	start := utils.ConvertH256ToUint256Int(req.StartBlockNumber).Uint64()
	if p.pid != pid || p.start != start || p.count != req.Count || req.Step != 1 {
		p.cancel()
		return nil
	}
	return p
}

// dropCatchUpPrefetch cancels a pending prefetch (end of a catch-up).
func (s *Service) dropCatchUpPrefetch() {
	s.catchUpPrefetch.mu.Lock()
	p := s.catchUpPrefetch.cur
	s.catchUpPrefetch.cur = nil
	s.catchUpPrefetch.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}

// catchUpFetch is SendBodiesByRangeRequest for the catch-up round, answered
// by the matching prefetch when there is one. A prefetch that failed is not
// reported: the request is simply sent again, so the peer's record and the
// round see exactly one outcome per request.
func (s *Service) catchUpFetch(ctx context.Context, pid peer.ID, req *sync_pb.BodiesByRangeRequest) ([]*block.Block, error) {
	if p := s.takeCatchUpPrefetch(pid, req); p != nil {
		select {
		case <-p.done:
			if p.err == nil && len(p.blocks) > 0 {
				return p.blocks, nil
			}
			log.Debug("hotstuff catch-up: prefetch failed, requesting again", "peer", shortPeer(pid), "err", p.err)
		case <-ctx.Done():
			p.cancel()
			return nil, ctx.Err()
		}
	}
	return SendBodiesByRangeRequest(ctx, s.cfg.chain, s.cfg.p2p, pid, req, nil)
}
