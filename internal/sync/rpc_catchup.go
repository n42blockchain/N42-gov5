// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common"
	block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/proto/sync_pb"
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
	self := currentBlockNumber(s.cfg.chain)
	highest, peers := s.cfg.p2p.Peers().BestPeers(5, self)
	if highest == nil || len(peers) == 0 || highest.Cmp(self) <= 0 {
		return // nobody is ahead of us
	}
	s.catchUpTo(highest.Uint64(), peers)
}

// CatchUpTo pulls through an authenticated consensus target without depending
// on remote heights cached during the P2P status handshake. HotStuff can
// advance several dozen blocks before the periodic status refresh, so a
// restarted validator otherwise catches only to that stale advertised height
// and then falls farther behind while new CommitQCs continue arriving.
//
// The caller obtains target from a verified CommitQC. Range responses still
// undergo the normal block validation/import path; the target only selects how
// far to ask connected peers for data.
func (s *Service) CatchUpTo(target uint64) {
	self := currentBlockNumber(s.cfg.chain)
	if self == nil || target <= self.Uint64() {
		return
	}
	peers := s.cfg.p2p.Peers().Connected()
	if len(peers) > 5 {
		peers = peers[:5]
	}
	if len(peers) == 0 {
		return
	}
	s.catchUpTo(target, peers)
}

func (s *Service) catchUpTo(target uint64, peers []peer.ID) {
	if !catchUpInProgress.CompareAndSwap(false, true) {
		return // a catch-up is already running
	}
	defer catchUpInProgress.Store(false)

	self := currentBlockNumber(s.cfg.chain)
	if self == nil || target <= self.Uint64() {
		return
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
	// Deep-fork backtrack: a node that applied MORE than one losing block
	// (view-change churn) rejects the fetched range with unknown-ancestor —
	// the winning sibling at start-1 is not in the range, so its parent is
	// never importable. Walk the start down one block per unknown-ancestor
	// failure (bounded well inside the 256-block undo window) until the range
	// reaches back to the common ancestor. Only meaningful on the authorized
	// path; a plain height lag never unwinds.
	const maxForkBacktrack = 32
	for attempt := 0; attempt <= maxForkBacktrack; attempt++ {
		if start > target {
			return
		}
		count := target - start + 1
		if count > maxRequestBlocks {
			count = maxRequestBlocks
		}
		req := &sync_pb.BodiesByRangeRequest{
			StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(start)),
			Count:            count,
			Step:             1,
		}
		log.Info("hotstuff catch-up: requesting range",
			"from", start, "to", target, "self", self.Uint64(), "peers", len(peers), "attempt", attempt)
		if s.catchUpRange(req, peers, authorized, start, target) {
			return
		}
		if !authorized || start <= 1 {
			return
		}
		start-- // unknown-ancestor on every peer: reach one block deeper
	}
}

// catchUpRange fetches [start, highest] from the given peers and inserts it.
// Returns true when the round is DONE (imported, or failed for a reason that
// deeper backtracking cannot fix); false when every peer failed with
// unknown-ancestor and the caller should retry one block lower.
func (s *Service) catchUpRange(req *sync_pb.BodiesByRangeRequest, peers []peer.ID, authorized bool, start, highest uint64) bool {
	sawUnknownAncestor := false
	for _, pid := range peers {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		fetched, err := SendBodiesByRangeRequest(ctx, s.cfg.chain, s.cfg.p2p, pid, req, nil)
		cancel()
		if err != nil {
			log.Warn("hotstuff catch-up: range request failed",
				"peer", pid.String()[:12], "from", start, "to", highest, "err", err)
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
		blocks, imported, ierr := s.insertCatchUpBlocks(s.ctx, blocks, authorized)
		if len(blocks) == 0 {
			log.Debug("hotstuff catch-up: dropped known bad range", "peer", pid.String()[:12])
			continue
		}
		// A taller fetched chain reorgs us off the fork. In the equal-height-fork
		// case we insert with branch-switch authority so the losing applied
		// sibling is reverted; otherwise plain InsertChain (which refuses to
		// revert an applied branch) suffices for a pure height lag.
		if ierr != nil {
			if errors.Is(ierr, consensus.ErrUnknownAncestor) ||
				strings.Contains(ierr.Error(), "sibling parent not applied") ||
				strings.Contains(ierr.Error(), "unknown ancestor") {
				sawUnknownAncestor = true
			}
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
		return true
	}
	// Every peer failed. Only a consistent unknown-ancestor is fixable by
	// reaching one block deeper; anything else ends the round (the next tick
	// retries from scratch).
	return !sawUnknownAncestor
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
