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
			log.Warn("hotstuff catch-up: range request failed",
				"peer", pid.String()[:12], "from", start, "to", highest.Uint64(), "err", err)
			continue
		}
		if len(fetched) == 0 {
			log.Warn("hotstuff catch-up: range returned 0 blocks", "peer", pid.String()[:12])
			continue
		}
		// Drop nil entries defensively — a partially decoded chunk stream can
		// leave holes, and a nil block panics every downstream .Hash()/.Number64()
		// (observed live: the catch-up tick panic-recover looping every 8s).
		blocks := make([]block.IBlock, 0, len(fetched))
		for _, b := range fetched {
			if b != nil {
				blocks = append(blocks, b)
			}
		}
		if len(blocks) == 0 {
			log.Warn("hotstuff catch-up: range contained only nil blocks", "peer", pid.String()[:12])
			continue
		}
		// A taller fetched chain reorgs us off the fork. In the equal-height-fork
		// case we insert with branch-switch authority so the losing applied
		// sibling is reverted; otherwise plain InsertChain (which refuses to
		// revert an applied branch) suffices for a pure height lag.
		var ierr error
		var imported int
		if authorized {
			imported, ierr = s.insertAuthorized(blocks)
		} else {
			imported, ierr = s.cfg.chain.InsertChain(blocks)
		}
		if ierr != nil {
			log.Warn("hotstuff catch-up: insert failed", "err", ierr, "authorized", authorized,
				"from", blocks[0].Number64().Uint64(), "count", len(blocks), "imported", imported)
			// A PARTIAL import still advanced the applied head. Canonicalize the
			// imported prefix (via its tail block's embedded CommitQC) so the
			// next round starts ABOVE it — otherwise the canonical head stays
			// put and every 8s tick unwinds and re-executes the same prefix
			// (observed live: +1 block per round with a full replay each time).
			// If the guessed tail didn't actually land, CommitToCanonical skips
			// it harmlessly (block not in DB).
			if n := s.cfg.blockImportNotifier; n != nil && imported > 0 && imported <= len(blocks) {
				tail := blocks[imported-1]
				n.NotifyBlockImported(tail.Hash(), tail.TxHash())
			}
			continue
		}
		// Notify the engine about the imported range tail. Import paths no longer
		// touch the canonical table (commit-authority-only), so a catching-up
		// node advances its committed head exclusively via the header-embedded
		// CommitQC processed by NotifyBlockImported; the commit walk back-fills
		// the whole imported range from the tail block's QC.
		if n := s.cfg.blockImportNotifier; n != nil {
			tail := blocks[len(blocks)-1]
			n.NotifyBlockImported(tail.Hash(), tail.TxHash())
		}
		var newHeadNum uint64
		if newHead := currentBlockNumber(s.cfg.chain); newHead != nil {
			newHeadNum = newHead.Uint64()
		}
		log.Info("hotstuff catch-up: imported range",
			"fetched", len(blocks), "newHead", newHeadNum, "authorized", authorized)
		return
	}
}

// HeightBehind reports how many blocks the local head trails the best peer,
// or 0 when synced, ahead, or no peer height is known yet. It is the block-
// production sync-gate signal for the HotStuff service (hotstuff.BlockFetcher):
// a validator this far behind must catch up before producing, or it builds on a
// stale head and self-forks. Uses the same BestPeers height source as CatchUp.
func (s *Service) HeightBehind() uint64 {
	self := currentBlockNumber(s.cfg.chain)
	highest, peers := s.cfg.p2p.Peers().BestPeers(5, self)
	if highest == nil || len(peers) == 0 || highest.Cmp(self) <= 0 {
		return 0
	}
	return highest.Uint64() - self.Uint64()
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
