package sync

import (
	"context"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/log"
)

// blockSubscriber handles incoming block messages from gossip.
// Future blocks (ahead of the current chain tip) are queued; all others
// are inserted immediately. After successful import, the HotStuff consensus
// engine is notified so it can proceed with voting on the proposed block.
func (s *Service) blockSubscriber(ctx context.Context, msg proto.Message) error {
	blk := new(block.Block)
	if err := blk.FromProtoMessage(msg); err != nil {
		return err
	}
	blockNumber, err := requireBlockNumber(blk, "block number unavailable")
	if err != nil {
		return err
	}

	header := blk.Header()
	blockHash := header.Hash()
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

	if _, err := s.cfg.chain.InsertChain([]block.IBlock{blk}); err != nil {
		// If parent is missing, queue as future block instead of marking bad.
		// The parent may arrive moments later via gossip.
		// Check by message substring since multiple packages define independent
		// ErrUnknownAncestor sentinels (internal, consensus, consensus/misc).
		if isAncestorError(err) {
			log.Debug("Block parent not yet available, queuing as future",
				"number", blockNumber.Uint64(), "hash", blockHash)
			return s.cfg.chain.AddFutureBlock(blk)
		}
		s.setBadBlock(ctx, blk.Hash())
		return err
	}

	// Notify HotStuff consensus that this block is now locally available.
	// This allows validators to vote on proposals that reference this block.
	if n := s.cfg.blockImportNotifier; n != nil {
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
