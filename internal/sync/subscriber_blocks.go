package sync

import (
	"context"
	"errors"
	"strings"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
)

// blockSubscriber handles incoming block messages from gossip.
// Future blocks (ahead of the current chain tip) are queued; all others
// are inserted immediately. After successful import, the HotStuff consensus
// engine is notified so it can proceed with voting on the proposed block.
func (s *Service) blockSubscriber(ctx context.Context, data any) error {
	blk, ok := data.(*block.Block)
	if !ok {
		return errWrongMessage
	}
	blockNumber, err := requireBlockNumber(blk, "block number unavailable")
	if err != nil {
		return err
	}

	header := blk.Header()
	blockHash := header.Hash()
	s.observeCatchUpBlock(blockHash, blockNumber.Uint64())
	log.Debug("Subscriber received new block",
		"number", blockNumber.Uint64(),
		"hash", blockHash,
		"txs", len(blk.Transactions()),
	)

	currentHeight, err := requireCurrentBlockNumber(s.cfg.chain, "current block number unavailable")
	if err != nil {
		return err
	}
	if blockNumber.Uint64() > currentHeight.Uint64()+1 {
		return s.cfg.chain.AddFutureBlock(blk)
	}

	// Already stored (typically a re-gossiped uncommitted leader block, resent
	// once per failing view by every peer). Do NOT re-enter InsertChain: for a
	// non-canonical same-height sibling it takes the reorg path on every arrival,
	// which — at tens of hits a second — starves the import pipeline. Canonical
	// selection is a consensus decision (CommitToCanonical), not a gossip one.
	// Notify the engine (idempotently, deduped downstream) only when the block
	// is actually APPLIED — body presence alone must not unlock a vote.
	if s.cfg.chain.HasBlock(blockHash, blockNumber.Uint64()) {
		if n := s.cfg.blockImportNotifier; n != nil && s.blockApplied(blockHash, blockNumber.Uint64()) {
			n.NotifyBlockImported(blockHash, blk.TxHash())
		}
		return nil
	}

	if _, err := s.cfg.chain.InsertChain([]block.IBlock{blk}); err != nil {
		// If parent is missing, queue as future block instead of marking bad.
		// The parent may arrive moments later via gossip.
		// Check by message substring since multiple packages define independent
		// ErrUnknownAncestor sentinels (internal, consensus, consensus/misc).
		if isAncestorError(err) {
			log.Debug("Block parent not yet available, queuing as future",
				"number", blockNumber.Uint64(), "hash", blockHash)
			// Actively fetch the missing parent by hash: it can be a committed
			// same-height sibling from an already-passed view that will never
			// be gossiped again — waiting passively leaves the queued child
			// (and the whole chain) stuck forever. No-op if it arrives first.
			s.FetchBlockByHash(blk.ParentHash())
			return s.cfg.chain.AddFutureBlock(blk)
		}
		if errors.Is(err, consensus.ErrExecutionInvalid) {
			s.setBadBlock(ctx, blk.Hash())
		}
		return err
	}

	// Notify HotStuff consensus that this block is now locally available.
	// This allows validators to vote on proposals that reference this block.
	// A nil insert error is not proof of execution (future-queued blocks
	// return nil too) — require applied-state evidence.
	if n := s.cfg.blockImportNotifier; n != nil && s.blockApplied(blockHash, blockNumber.Uint64()) {
		n.NotifyBlockImported(blockHash, blk.TxHash())
	}

	return nil
}

// isAncestorError checks if the error indicates a missing parent block.
// Uses substring matching because multiple packages define independent
// ErrUnknownAncestor / ErrPrunedAncestor sentinels.
func isAncestorError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unknown ancestor") || strings.Contains(msg, "pruned ancestor")
}
