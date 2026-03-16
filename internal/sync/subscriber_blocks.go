package sync

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/log"
)

// blockSubscriber handles incoming block messages from gossip.
// Future blocks (ahead of the current chain tip) are queued; all others
// are inserted immediately.
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
	log.Info("Subscriber received new block",
		"number", blockNumber.Uint64(),
		"hash", header.Hash(),
		"stateRoot", header.StateRoot(),
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
	return nil
}
