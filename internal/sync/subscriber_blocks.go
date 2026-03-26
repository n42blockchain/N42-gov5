package sync

import (
	"context"

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
