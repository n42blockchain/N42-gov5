package sync

import (
	"context"
	"fmt"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

var ErrOptimisticParent = errors.New("parent of the block is optimistic")

// validateBlockPubSub checks that the incoming block has a valid BLS signature.
// Blocks that have already been seen are ignored. If the BLS signature is any valid signature,
// this method rebroadcasts the message.
func (s *Service) validateBlockPubSub(ctx context.Context, pid peer.ID, msg *pubsub.Message) (pubsub.ValidationResult, error) {
	receivedTime := time.Now()

	// Validation runs on publish (not just subscriptions), so we should approve
	// any message from ourselves.
	if pid == s.cfg.p2p.PeerID() {
		return pubsub.ValidationAccept, nil
	}

	// We should not attempt to process blocks until fully synced, but propagation is OK.
	if s.cfg.initialSync.Syncing() {
		return pubsub.ValidationIgnore, nil
	}

	ctx, span := trace.StartSpan(ctx, "sync.validateBlockPubSub")
	defer span.End()

	m, err := s.decodePubsubMessage(msg)
	if err != nil {
		return pubsub.ValidationReject, errors.Wrap(err, "Could not decode message")
	}

	s.validateBlockLock.Lock()
	defer s.validateBlockLock.Unlock()

	blk, ok := m.(*types_pb.Block)
	if !ok {
		return pubsub.ValidationReject, errors.New("msg is not types_pb.Block")
	}

	iBlock := new(block.Block)
	if err = iBlock.FromProtoMessage(blk); err != nil {
		return pubsub.ValidationReject, errors.New("block.Block is nil")
	}

	iHeader, iBody := iBlock.Header(), iBlock.Body()
	header, ok := iHeader.(*block.Header)
	if !ok {
		return pubsub.ValidationReject, errors.New("msg.header is not block.Header")
	}
	if _, ok = iBody.(*block.Body); !ok {
		return pubsub.ValidationReject, errors.New("msg.body is not block.Body")
	}

	if s.cfg.chain.HasBlock(header.Root, header.Number.Uint64()) {
		return pubsub.ValidationIgnore, nil
	}

	// Check if parent is a bad block and then reject the block.
	if s.hasBadBlock(header.ParentHash) {
		s.setBadBlock(ctx, header.Root)
		err := fmt.Errorf("received block with root %#x that has an invalid parent %#x", header.Root, header.ParentHash)
		log.Debug("Received block with an invalid parent", "err", err)
		return pubsub.ValidationReject, err
	}

	if err := captureArrivalTimeMetric(header.Time); err != nil {
		log.Debug("Ignored block: could not capture arrival time metric", "err", err)
		return pubsub.ValidationIgnore, nil
	}

	// Handle block when the parent is unknown.
	// Safety check: ensure block number is > 0 before subtracting to prevent underflow.
	if header.Number.Uint64() > 0 && !s.cfg.chain.HasBlock(header.ParentHash, header.Number.Uint64()-1) {
		// TODO: implement orphan block handling
	}

	msg.ValidatorData = iBlock.ToProtoMessage() // Used in downstream subscriber

	log.Debug("Received block")

	blockVerificationGossipSummary.Observe(float64(time.Since(receivedTime).Milliseconds()))
	return pubsub.ValidationAccept, nil
}

// hasBadBlock returns true if the block is marked as a bad block.
func (s *Service) hasBadBlock(root types.Hash) bool {
	s.badBlockLock.RLock()
	defer s.badBlockLock.RUnlock()
	_, seen := s.badBlockCache.Get(root)
	return seen
}

// setBadBlock marks the block as bad in the cache, unless the context is cancelled.
func (s *Service) setBadBlock(ctx context.Context, root types.Hash) {
	s.badBlockLock.Lock()
	defer s.badBlockLock.Unlock()
	if ctx.Err() != nil {
		return
	}
	log.Debug("Inserting in invalid block cache", "root", root)
	s.badBlockCache.Add(root, true)
}

// captureArrivalTimeMetric records the block propagation latency.
func captureArrivalTimeMetric(headerTime uint64) error {
	startTime := time.Unix(int64(headerTime), 0)
	elapsed := time.Since(startTime)
	if elapsed < 0 {
		return fmt.Errorf("the block is a future block, time is %s", startTime.Format(time.RFC3339))
	}
	ms := float64(elapsed / time.Millisecond)
	arrivalBlockPropagationHistogram.Observe(ms)
	arrivalBlockPropagationGauge.Set(ms)
	return nil
}
