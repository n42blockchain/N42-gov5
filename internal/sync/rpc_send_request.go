package sync

import (
	"context"
	"io"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/common"
	types "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// ErrInvalidFetchedData is thrown if stream fails to provide requested blocks.
var ErrInvalidFetchedData = errors.New("invalid data returned from peer")

// BlockProcessor defines a block processing function, which allows to start utilizing
// blocks even before all blocks are ready.
type BlockProcessor func(block *types.Block) error

// SendBodiesByRangeRequest sends BeaconBlocksByRange and returns fetched blocks, if any.
func SendBodiesByRangeRequest(ctx context.Context, chain common.IBlockChain, p2pProvider p2p.SenderEncoder, pid peer.ID, req *sync_pb.BodiesByRangeRequest, blockProcessor BlockProcessor) ([]*types.Block, error) {
	return sendBodiesByRange(ctx, chain, p2pProvider, pid, req, blockProcessor, false)
}

// sendBodiesByRange is SendBodiesByRangeRequest; with cutOnCancel the stream
// is reset as soon as ctx ends, so a timeout also bounds a slow transfer and
// the blocks received before it are kept (the catch-up pipeline). Without it
// ctx bounds only opening the stream, as before.
func sendBodiesByRange(ctx context.Context, chain common.IBlockChain, p2pProvider p2p.SenderEncoder, pid peer.ID, req *sync_pb.BodiesByRangeRequest, blockProcessor BlockProcessor, cutOnCancel bool) ([]*types.Block, error) {
	if req == nil || req.Step == 0 || req.Step > rangeLimit || req.Count == 0 || req.Count > maxRequestBlocks {
		return nil, errors.New("invalid range count or step")
	}
	blockStart := utils.ConvertH256ToUint256Int(req.StartBlockNumber)
	span := req.Step * (req.Count - 1)
	blockEnd, overflow := new(uint256.Int).AddOverflow(blockStart, uint256.NewInt(span))
	if overflow || span > rangeLimit {
		return nil, errors.New("requested range exceeds bounds")
	}

	// v1.1: prefer /2 (committed-only, byte budget, END trailer) when the
	// peer lists it in identify; fall back to /1 when it does not or when
	// multistream refuses it (the peer is then marked v1-only for an hour).
	if h := hostOf(p2pProvider); req.Step == 1 && peerSpeaks(h, pid, syncwire.ProtocolBodiesByRangeV2) {
		blocks, err := rangeV2Blocks(ctx, h, pid, blockStart.Uint64(), req.Count)
		if err == nil {
			if blockProcessor != nil {
				for _, b := range blocks {
					if perr := blockProcessor(b); perr != nil {
						return nil, perr
					}
				}
			}
			return blocks, nil
		}
		if !isProtocolNotSupported(err) {
			return nil, err
		}
		log.Debug("bodies-by-range/2 refused by multistream, using /1", "peer", pid.String())
	}

	topic, err := p2p.TopicFromMessage(p2p.BodiesByRangeMessageName)
	if err != nil {
		return nil, err
	}
	stream, err := p2pProvider.Send(ctx, &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(utils.ConvertH256ToUint256Int(req.StartBlockNumber)),
		Count:            req.Count,
		Step:             req.Step,
	}, topic, pid)
	if err != nil {
		if isProtocolNotSupported(err) {
			// The peer refuses /1 too (§5.0): catch-up skips it for range
			// requests while the mark lasts. Not a fault, so no score.
			v11MarkUnsupported(pid, rangeV1Protocol(p2pProvider))
		}
		return nil, err
	}
	defer closeStream(stream)
	if cutOnCancel {
		stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
		defer stop()
	}

	blocks := make([]*types.Block, 0, req.Count)
	process := func(blk *types.Block) error {
		blocks = append(blocks, blk)
		if blockProcessor != nil {
			return blockProcessor(blk)
		}
		return nil
	}

	var prevBlockNr *uint256.Int

	for i := uint64(0); ; i++ {
		isFirstChunk := i == 0
		blk, err := ReadChunkedBlock(stream, p2pProvider, isFirstChunk)
		if errors.Is(err, io.EOF) {
			log.Debug("Received blocks from peer", "count", i, "requested", req.Count, "start", blockStart.Uint64(), "peer", pid.String())
			break
		}
		if err != nil {
			// A server that fails after streaming a prefix (gov5 main and
			// n42-26 append an error chunk; a stream may also be cut) has
			// still delivered valid blocks. Keep them: the caller imports the
			// prefix and the next request starts after it. Only a failure
			// before the first block is a failed range.
			if len(blocks) > 0 {
				log.Debug("Range reply failed after a prefix, keeping the blocks received",
					"blocks", len(blocks), "requested", req.Count, "start", blockStart.Uint64(), "peer", pid.String(), "err", err)
				break
			}
			return nil, err
		}

		// The response MUST contain no more than `count` blocks, and no more than
		// MAX_REQUEST_BLOCKS blocks.
		if i >= req.Count || i >= maxRequestBlocks {
			return nil, ErrInvalidFetchedData
		}

		blockNr := blk.Number64()

		// Returned blocks must belong to the requested arithmetic progression.
		if blockNr.Cmp(blockStart) < 0 || blockNr.Cmp(blockEnd) > 0 {
			return nil, ErrInvalidFetchedData
		}

		offsetFromStart := new(uint256.Int).Sub(blockNr, blockStart)
		if new(uint256.Int).Mod(offsetFromStart, uint256.NewInt(req.Step)).Sign() != 0 {
			return nil, ErrInvalidFetchedData
		}

		// Returned blocks must be in consecutive order with values in `step` increments.
		if !isFirstChunk && prevBlockNr != nil {
			if prevBlockNr.Cmp(blockNr) >= 0 {
				return nil, ErrInvalidFetchedData
			}
			offset := new(uint256.Int).Sub(blockNr, prevBlockNr)
			if new(uint256.Int).Mod(offset, uint256.NewInt(req.Step)).Sign() != 0 {
				return nil, ErrInvalidFetchedData
			}
		}

		prevBlockNr = blockNr.Clone()
		if err := process(blk); err != nil {
			return nil, err
		}
	}

	return blocks, nil
}
