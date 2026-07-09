// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common"
	block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/log"
)

// hotStuffExtraMagic is the 4-byte prefix HotStuff writes into every consensus
// block's header extra-data (mirrors hotstuff.extraMagic "N42H"). A local head
// carrying it is a consensus block whose same-height sibling can be switched to;
// blocks below the consensus start height are plain replay blocks without it.
var hotStuffExtraMagic = []byte("N42H")

// headIsConsensusBlock reports whether the local head is a HotStuff consensus
// block (extra-data begins with the N42H magic). Checked inline to avoid a
// sync -> consensus/hotstuff import.
func headIsConsensusBlock(chain common.IBlockChain) bool {
	head := chain.CurrentBlock()
	if head == nil {
		return false
	}
	h, ok := head.Header().(*block.Header)
	if !ok || len(h.Extra) < len(hotStuffExtraMagic) {
		return false
	}
	return string(h.Extra[:len(hotStuffExtraMagic)]) == string(hotStuffExtraMagic)
}

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

	// Default (pure height lag): pull only the blocks above our head.
	start := self.Uint64() + 1
	authorized := false
	// Equal-height fork recovery: when our head is a HotStuff consensus block
	// (carries the N42H magic), a losing applied sibling can be reverted onto the
	// converged branch. Start ONE block lower so the fetched range includes the
	// WINNING sibling at our head height, and insert WITH branch-switch authority
	// so ForkChoice unwinds our losing sibling. Without this, a node that
	// committed a view on a losing same-height sibling fetches head+1 forever —
	// its parent (the winning sibling) is never requested, so the range never
	// connects and the head is pinned (observed: a node stuck on a 13013144
	// sibling re-requesting 13013145.. indefinitely). Replay blocks below the
	// consensus start height lack the magic and keep the head+1 path, since
	// re-inserting one trips VerifyHeader -> BAD BLOCK. Re-inserting our own
	// canonical head (the no-fork case) is a harmless already-known skip.
	if self.Uint64() > 0 && headIsConsensusBlock(s.cfg.chain) {
		start = self.Uint64()
		authorized = true
	}
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
		// A taller fetched chain reorgs us off the fork. In the equal-height-fork
		// case we insert with branch-switch authority so the losing applied
		// sibling is reverted; otherwise plain InsertChain (which refuses to
		// revert an applied branch) suffices for a pure height lag.
		var ierr error
		if authorized {
			_, ierr = s.insertAuthorized(blocks)
		} else {
			_, ierr = s.cfg.chain.InsertChain(blocks)
		}
		if ierr != nil {
			log.Debug("hotstuff catch-up: insert failed", "err", ierr, "authorized", authorized)
			continue
		}
		newHead := currentBlockNumber(s.cfg.chain)
		log.Info("hotstuff catch-up: imported range",
			"fetched", len(blocks), "newHead", newHead.Uint64(), "authorized", authorized)
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
