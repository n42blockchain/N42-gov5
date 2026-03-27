package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/holiman/uint256"
	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/proto/sync_pb"
	types "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/common/utils"
)

// bodiesByRangeRPCHandler looks up the requested blocks from the database from a given start block.
func (s *Service) bodiesByRangeRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.BodiesByRangeHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	// Ticker to stagger out large requests.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	m, ok := msg.(*sync_pb.BodiesByRangeRequest)
	if !ok {
		return errors.New("message is not type *pb.BeaconBlockByRangeRequest")
	}
	if err := s.validateRangeRequest(m); err != nil {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, err.Error(), stream)
		s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(stream.Conn().RemotePeer())
		return err
	}

	// Check if requested range is within available history (EIP-4444).
	if s.cfg.earliestBlock != nil {
		earliest := s.cfg.earliestBlock()
		if earliest > 0 {
			startBlock := utils.ConvertH256ToUint256Int(m.StartBlockNumber).Uint64()
			if startBlock < earliest {
				reason := fmt.Sprintf("requested block %d is before earliest available block %d", startBlock, earliest)
				s.writeErrorResponseToStream(responseCodeInvalidRequest, reason, stream)
				return p2ptypes.ErrInvalidRequest
			}
		}
	}

	// Only process range requests with a step of 1.
	if m.Step > 1 {
		m.Step = 1
	}

	// Clamp the initial batch count to the allowed blocks-per-second limit.
	count := m.Count
	allowedBlocksPerSecond := uint64(s.cfg.p2p.GetConfig().P2PLimit.BlockBatchLimit)
	if count > allowedBlocksPerSecond {
		count = allowedBlocksPerSecond
	}

	startBlockNumber := utils.ConvertH256ToUint256Int(m.StartBlockNumber)

	// Check for potential integer overflow before multiplication.
	if m.Step > 0 && count > 1 && m.Step > (^uint64(0))/(count-1) {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "step*count overflow", stream)
		return p2ptypes.ErrInvalidRequest
	}
	endBlockNumber := new(uint256.Int).AddUint64(startBlockNumber, m.Step*(count-1))

	// The final requested block from the remote peer.
	endReqBlockNumber := new(uint256.Int).AddUint64(startBlockNumber, m.Step*(m.Count-1))

	blockLimiter, err := s.rateLimiter.topicCollector(string(stream.Protocol()))
	if err != nil {
		return err
	}
	remainingBucketCapacity := blockLimiter.Remaining(stream.Conn().RemotePeer().String())
	span.AddAttributes(
		trace.Int64Attribute("start", int64(startBlockNumber.Uint64())),
		trace.Int64Attribute("end", int64(endReqBlockNumber.Uint64())),
		trace.Int64Attribute("step", int64(m.Step)),
		trace.Int64Attribute("count", int64(m.Count)),
		trace.StringAttribute("peer", stream.Conn().RemotePeer().String()),
		trace.Int64Attribute("remaining_capacity", remainingBucketCapacity),
	)

	for startBlockNumber.Cmp(endReqBlockNumber) <= 0 {
		if err := s.rateLimiter.validateRequest(stream, allowedBlocksPerSecond); err != nil {
			return err
		}

		if new(uint256.Int).Sub(endBlockNumber, startBlockNumber).Uint64() > rangeLimit {
			s.writeErrorResponseToStream(responseCodeInvalidRequest, p2ptypes.ErrInvalidRequest.Error(), stream)
			return p2ptypes.ErrInvalidRequest
		}

		err := s.writeBodiesRangeToStream(ctx, startBlockNumber, endBlockNumber, m.Step, stream)
		if err != nil && !errors.Is(err, p2ptypes.ErrInvalidParent) {
			return err
		}

		// Decrease allowed blocks capacity by the number of streamed blocks.
		if startBlockNumber.Cmp(endBlockNumber) <= 0 {
			blocksStreamed := new(uint256.Int).Div(
				new(uint256.Int).Sub(endBlockNumber, startBlockNumber),
				new(uint256.Int).SetUint64(m.Step),
			)
			s.rateLimiter.add(stream, int64(1+blocksStreamed.Uint64()))
		}

		// Exit if we have a disjoint chain to return.
		if errors.Is(err, p2ptypes.ErrInvalidParent) {
			break
		}

		// Recalculate start and end for the next batch.
		startBlockNumber = new(uint256.Int).AddUint64(endBlockNumber, m.Step)
		endBlockNumber = new(uint256.Int).AddUint64(startBlockNumber, m.Step*(allowedBlocksPerSecond-1))
		if endBlockNumber.Cmp(endReqBlockNumber) > 0 {
			endBlockNumber = endReqBlockNumber
		}

		// All blocks have been sent; no need to wait.
		if startBlockNumber.Cmp(endReqBlockNumber) > 0 {
			break
		}

		// Wait for ticker before resuming streaming blocks to remote peer.
		<-ticker.C
	}

	closeStream(stream)
	return nil
}

func (s *Service) writeBodiesRangeToStream(ctx context.Context, startSlot, endSlot *uint256.Int, step uint64, stream libp2pcore.Stream) error {
	_, span := trace.StartSpan(ctx, "sync.WriteBodiesRangeToStream")
	defer span.End()

	blks := make([]types.IBlock, 0)
	for ; startSlot.Cmp(endSlot) <= 0; startSlot = new(uint256.Int).AddUint64(startSlot, step) {
		b, err := s.cfg.chain.GetBlockByNumber(startSlot)
		if err != nil {
			log.Warn("Could not retrieve blocks", "err", err)
			s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrGeneric.Error(), stream)
			return err
		}
		if b == nil {
			err := fmt.Errorf("block #%d not found", startSlot.Uint64())
			log.Warn("Could not retrieve blocks", "err", err)
			s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrInvalidBlockNr.Error(), stream)
			return err
		}
		blks = append(blks, b)
	}

	start := time.Now()
	for _, b := range blks {
		if chunkErr := s.chunkBlockWriter(stream, b); chunkErr != nil {
			log.Debug("Could not send a chunked response", "err", chunkErr)
			s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrGeneric.Error(), stream)
			return chunkErr
		}
	}
	rpcBlocksByRangeResponseLatency.Observe(float64(time.Since(start).Milliseconds()))

	return nil
}

func (s *Service) validateRangeRequest(r *sync_pb.BodiesByRangeRequest) error {
	startSlot := utils.ConvertH256ToUint256Int(r.StartBlockNumber)
	count := r.Count
	step := r.Step

	// Add a buffer for possible large range requests from nodes syncing close to the head.
	buffer := rangeLimit * 2
	highestExpectedBlockNumber := new(uint256.Int).AddUint64(currentBlockNumber(s.cfg.chain), uint64(buffer))

	if count == 0 || count > maxRequestBlocks {
		return p2ptypes.ErrInvalidRequest
	}
	if step == 0 || step > rangeLimit {
		return p2ptypes.ErrInvalidRequest
	}
	if startSlot.Cmp(highestExpectedBlockNumber) > 0 {
		return p2ptypes.ErrInvalidRequest
	}

	endSlot := new(uint256.Int).AddUint64(startSlot, step*(count-1))
	if endSlot.Uint64()-startSlot.Uint64() > rangeLimit {
		return p2ptypes.ErrInvalidRequest
	}
	return nil
}

func (s *Service) writeErrorResponseToStream(responseCode byte, reason string, stream libp2pcore.Stream) {
	writeErrorResponseToStream(responseCode, reason, stream, s.cfg.p2p)
}
