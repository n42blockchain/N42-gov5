package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/holiman/uint256"
	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	types "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/proto/sync_pb"
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
		// A requester that is merely ahead of our head (a far-behind peer
		// probing a lagging server) is behaving legitimately: refuse with code
		// 1 but do not score it. Malformed counts and steps still are.
		if !errors.Is(err, errRangeAheadOfHead) {
			s.cfg.p2p.Peers().Scorers().BadResponsesScorer().Increment(stream.Conn().RemotePeer())
		}
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

	// Clamp the initial batch count to the allowed blocks-per-second limit.
	count := m.Count
	allowedBlocksPerSecond := uint64(maxRequestBlocks)
	if cfg := s.cfg.p2p.GetConfig(); cfg != nil && cfg.P2PLimit != nil && cfg.P2PLimit.BlockBatchLimit > 0 {
		allowedBlocksPerSecond = uint64(cfg.P2PLimit.BlockBatchLimit)
	}
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

	// reply records what earlier sub-batches already streamed, so a later
	// sub-batch that finds nothing stored (or hits a cap or a failure) ends
	// the response cleanly instead of appending an error to a valid prefix.
	reply := &rangeReply{}
	for startBlockNumber.Cmp(endReqBlockNumber) <= 0 {
		if reply.blocks > 0 {
			// A prefix is already on the wire. Running out of rate-limit
			// budget now ends the reply cleanly: validateRequest would
			// reset the stream and score the peer, discarding the prefix.
			if !s.cfg.p2p.Peers().IsTrusted(stream.Conn().RemotePeer()) &&
				blockLimiter.Remaining(stream.Conn().RemotePeer().String()) < int64(allowedBlocksPerSecond) {
				log.Debug("bodies-by-range: rate budget spent after a prefix, ending the reply cleanly",
					"peer", stream.Conn().RemotePeer().String(), "blocks", reply.blocks)
				break
			}
		} else if err := s.rateLimiter.validateRequest(stream, allowedBlocksPerSecond); err != nil {
			return err
		}

		if new(uint256.Int).Sub(endBlockNumber, startBlockNumber).Uint64() > rangeLimit {
			s.writeErrorResponseToStream(responseCodeInvalidRequest, p2ptypes.ErrInvalidRequest.Error(), stream)
			return p2ptypes.ErrInvalidRequest
		}

		err := s.writeBodiesRange(ctx, startBlockNumber, endBlockNumber, m.Step, reply, stream)
		if err != nil && !errors.Is(err, p2ptypes.ErrInvalidParent) && !errors.Is(err, errRangeTruncated) {
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

		// Exit if we have a disjoint chain to return, or the stored canonical
		// chain ended inside the requested range.
		if errors.Is(err, p2ptypes.ErrInvalidParent) || errors.Is(err, errRangeTruncated) {
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

		// Pace the next sub-batch only when the peer's bucket lacks capacity
		// for it — validateRequest rejects (not waits) on an empty bucket, so
		// streaming ahead of the refill would self-inflict ErrRateLimited.
		// Burst capacity (BlockBatchLimitBurstFactor×) lets several
		// sub-batches go back-to-back, and with the default limit
		// (== maxRequestBlocks) a request is a single batch and never waits;
		// the old unconditional tick stalled 1s after EVERY sub-batch, capping
		// serving at limit-per-second regardless of remaining capacity.
		if blockLimiter.Remaining(stream.Conn().RemotePeer().String()) < int64(allowedBlocksPerSecond) {
			<-ticker.C
		}
	}

	closeStream(stream)
	return nil
}

// errRangeTruncated reports that writeBodiesRangeToStream streamed a valid
// prefix of the requested sub-range and stopped early (the stored canonical
// chain ends, or breaks, inside it). The response is complete as sent; the
// caller must not stream further sub-batches.
var errRangeTruncated = errors.New("bodies-by-range: response truncated")

// rangeReplyByteCap bounds the decoded block bytes of one bodies_by_range
// reply. The common sync standard allows servers at most 128 MiB (and
// recommends 64); every client reads 128 MiB, so this is the largest value
// that is safe for all peers. At least one block is always served, however
// large. Before this cap a reply was bounded only by the block count.
var rangeReplyByteCap = uint64(128 << 20)

var rangeReplyCapLogOnce sync.Once

// rangeReply tracks what one bodies_by_range response has already streamed,
// across sub-batches. It decides how a failure is reported: before the first
// block a single error chunk is fine, after it the reply must end cleanly (an
// error chunk after a prefix makes the n42-rs and n42-26 readers discard the
// whole prefix).
type rangeReply struct {
	blocks int
	bytes  uint64
}

// writeBodiesRangeToStream streams the canonical blocks in [startSlot, endSlot].
// servedBefore says an earlier sub-batch already streamed blocks.
func (s *Service) writeBodiesRangeToStream(ctx context.Context, startSlot, endSlot *uint256.Int, step uint64, servedBefore bool, stream libp2pcore.Stream) error {
	reply := &rangeReply{}
	if servedBefore {
		reply.blocks = 1
	}
	return s.writeBodiesRange(ctx, startSlot, endSlot, step, reply, stream)
}

// writeBodiesRange streams the canonical blocks in [startSlot, endSlot].
// A missing block ends the response at the stored prefix: under deferred
// execution a peer writes canonical rows only after it imports a committed
// block, so a CommitQC-derived catch-up target routinely runs a few blocks
// past every peer's stored chain. Failing the whole range there (the old
// behaviour) left a lagging node with nothing to import from any peer. Only a
// response that would carry no block at all (nothing streamed yet, see
// reply) is still an error. The same holds for a read, encode or write
// failure and for the per-reply byte cap: once a block has been sent the
// reply ends cleanly with the blocks sent so far.
func (s *Service) writeBodiesRange(ctx context.Context, startSlot, endSlot *uint256.Int, step uint64, reply *rangeReply, stream libp2pcore.Stream) error {
	_, span := trace.StartSpan(ctx, "sync.WriteBodiesRangeToStream")
	defer span.End()

	blks := make([]types.IBlock, 0)
	truncated := false
	// Profile 1: ranges are committed-only. A number above the committed head
	// is answered as not stored (prefix served, or the usual miss error).
	ceiling, capped := s.committedServeCeiling()
	for ; startSlot.Cmp(endSlot) <= 0; startSlot = new(uint256.Int).AddUint64(startSlot, step) {
		var b types.IBlock
		var err error
		if capped && startSlot.GtUint64(ceiling) {
			log.Debug("bodies-by-range: request runs past the committed head, not serving above it",
				"at", startSlot.Uint64(), "committed", ceiling)
		} else {
			b, err = s.cfg.chain.GetBlockByNumber(startSlot)
		}
		if err != nil {
			log.Warn("Could not retrieve blocks", "err", err)
			if len(blks) > 0 || reply.blocks > 0 {
				truncated = true
				break
			}
			s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrGeneric.Error(), stream)
			return err
		}
		if b == nil && (len(blks) > 0 || reply.blocks > 0) {
			log.Debug("bodies-by-range: stored chain ends inside the range, serving the prefix",
				"at", startSlot.Uint64(), "end", endSlot.Uint64(), "served", len(blks))
			truncated = true
			break
		}
		if b == nil {
			err := fmt.Errorf("block #%d not found", startSlot.Uint64())
			log.Warn("Could not retrieve blocks", "err", err)
			s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrInvalidBlockNr.Error(), stream)
			return err
		}
		// Linkage guard (step 1 only): never serve a non-linked by-number
		// sequence. A canonical hole (dead-branch rows left by a historical
		// canonical writer) otherwise poisons the requester — its contiguity
		// check rejects the whole range and it retries forever (observed live:
		// a laggard spinning on a span whose canonical rows were three
		// different dead siblings). Truncate at the break; the linked prefix
		// is still importable, and the local canonical repair heals the table.
		if step == 1 && len(blks) > 0 && b.ParentHash() != blks[len(blks)-1].Hash() {
			log.Warn("bodies-by-range: canonical break, truncating response",
				"at", startSlot.Uint64(), "parent", b.ParentHash(), "prev", blks[len(blks)-1].Hash())
			truncated = true
			break
		}
		blks = append(blks, b)
	}

	start := time.Now()
	for _, b := range blks {
		data, encErr := encodeBlockChunkPayload(b)
		if encErr != nil {
			log.Debug("Could not encode a chunked response", "err", encErr)
			if reply.blocks > 0 {
				return errRangeTruncated
			}
			s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrGeneric.Error(), stream)
			return encErr
		}
		// Per-reply byte cap: end the reply cleanly rather than send more. The
		// first block is always sent.
		if reply.blocks > 0 && reply.bytes+uint64(len(data)) > rangeReplyByteCap {
			rangeReplyCapLogOnce.Do(func() {
				log.Info("bodies-by-range: reply byte cap reached, ending the reply cleanly (peers re-request from the next block)",
					"capMiB", rangeReplyByteCap>>20, "blocks", reply.blocks, "bytes", reply.bytes, "next", b.Number64().Uint64())
			})
			log.Debug("bodies-by-range: reply byte cap reached",
				"blocks", reply.blocks, "bytes", reply.bytes, "next", b.Number64().Uint64())
			return errRangeTruncated
		}
		if chunkErr := s.chunkEncodedBlockWriter(stream, b, data); chunkErr != nil {
			log.Debug("Could not send a chunked response", "err", chunkErr)
			// After a prefix the stream is cut mid-chunk or dead: an error
			// chunk would only follow partial bytes. Nothing sent yet: say so.
			if reply.blocks == 0 {
				s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrGeneric.Error(), stream)
			}
			return chunkErr
		}
		reply.blocks++
		reply.bytes += uint64(len(data))
	}
	rpcBlocksByRangeResponseLatency.Observe(float64(time.Since(start).Milliseconds()))

	if truncated {
		return errRangeTruncated
	}
	return nil
}

// errRangeAheadOfHead reports a request whose start lies beyond our head plus
// the look-ahead buffer. It still maps to p2ptypes.ErrInvalidRequest on the
// wire (code 1) but is never a scoring offence.
var errRangeAheadOfHead = fmt.Errorf("%w: start is ahead of the local head", p2ptypes.ErrInvalidRequest)

func (s *Service) validateRangeRequest(r *sync_pb.BodiesByRangeRequest) error {
	if r == nil {
		return p2ptypes.ErrInvalidRequest
	}
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
		return errRangeAheadOfHead
	}

	span := step * (count - 1)
	_, overflow := new(uint256.Int).AddOverflow(startSlot, uint256.NewInt(span))
	if overflow || span > rangeLimit {
		return p2ptypes.ErrInvalidRequest
	}
	return nil
}

func (s *Service) writeErrorResponseToStream(responseCode byte, reason string, stream libp2pcore.Stream) {
	writeErrorResponseToStream(responseCode, reason, stream, s.cfg.p2p)
}
