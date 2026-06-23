// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/proto/sync_pb"
	block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/log"
)

// catchUpInProgress guards against concurrent catch-up runs (the HotStuff engine
// can emit OutputSyncRequired on every high-view Decide while we are still
// importing).
var catchUpInProgress atomic.Bool

// catchUpInterval is how often a leader-driven node polls whether it has fallen
// behind peers in HEIGHT. A node that committed a view but couldn't import the
// block (or produced a startup fork) lags in height while its VIEW stays in
// sync, so the view-gap OutputSyncRequired never fires — height polling catches
// exactly that case.
const catchUpInterval = 8 * time.Second

// CatchUp pulls the canonical chain from the most-advanced peer by range and
// inserts it, letting ForkChoice reorg us off a startup fork onto the converged
// chain. It is invoked by the HotStuff engine via OutputSyncRequired (a Decide
// whose view is far ahead of ours means we are behind). Implements
// hotstuff.BlockFetcher.CatchUp.
func (s *Service) CatchUp() {
	if !catchUpInProgress.CompareAndSwap(false, true) {
		return // a catch-up is already running
	}
	defer catchUpInProgress.Store(false)

	self := currentBlockNumber(s.cfg.chain)
	highest, peers := s.cfg.p2p.Peers().BestPeers(5, self)
	if highest == nil || len(peers) == 0 || highest.Cmp(self) <= 0 {
		return // nobody is ahead of us
	}

	// Start just above our head: pull only the blocks we are missing. We do NOT
	// re-request our own head or earlier — those are either already imported or,
	// at/below the HotStuff start height, plain replay blocks without the N42H
	// consensus magic, and re-inserting one trips VerifyHeader -> BAD BLOCK,
	// failing the whole range. This recovers a node that is behind in HEIGHT
	// (committed a view but missed the block import); a true equal-height fork
	// would need an explicit unwind, which we don't attempt here.
	start := self.Uint64() + 1
	if start > highest.Uint64() {
		return
	}
	count := highest.Uint64() - start + 1
	if count > maxRequestBlocks {
		count = maxRequestBlocks
	}

	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(start)),
		Count:            count,
		Step:             1,
	}
	log.Info("hotstuff catch-up: requesting range",
		"from", start, "to", highest.Uint64(), "self", self.Uint64(), "peers", len(peers))

	for _, pid := range peers {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		fetched, err := SendBodiesByRangeRequest(ctx, s.cfg.chain, s.cfg.p2p, pid, req, nil)
		cancel()
		if err != nil {
			log.Debug("hotstuff catch-up: range request failed",
				"peer", pid.String()[:12], "from", start, "to", highest.Uint64(), "err", err)
			continue
		}
		if len(fetched) == 0 {
			log.Debug("hotstuff catch-up: range returned 0 blocks", "peer", pid.String()[:12])
			continue
		}
		blocks := make([]block.IBlock, 0, len(fetched))
		for _, b := range fetched {
			blocks = append(blocks, b)
		}
		// InsertChain triggers ForkChoice.ReorgNeeded; a taller fetched chain
		// reorgs us off the startup fork automatically.
		if _, err := s.cfg.chain.InsertChain(blocks); err != nil {
			log.Debug("hotstuff catch-up: insert failed", "err", err)
			continue
		}
		newHead := currentBlockNumber(s.cfg.chain)
		log.Info("hotstuff catch-up: imported range",
			"fetched", len(blocks), "newHead", newHead.Uint64())
		return
	}
}

// catchUpTick triggers a height-based catch-up if we lag peers. Gated to
// leader-driven (HotStuff) chains, which register a block import notifier;
// classic chains rely on InitialSync's resyncIfBehind. CatchUp self-guards
// against concurrent runs and is a no-op when we are not behind.
func (s *Service) catchUpTick() {
	if s.cfg.blockImportNotifier == nil {
		return
	}
	s.CatchUp()
}
