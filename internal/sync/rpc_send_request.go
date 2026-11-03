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
	if req == nil || req.Step == 0 || req.Step > rangeLimit || req.Count == 0 || req.Count > maxRequestBlocks {
		return nil, errors.New("invalid range count or step")
	}
	blockStart := utils.ConvertH256ToUint256Int(req.StartBlockNumber)
	span := req.Step * (req.Count - 1)
	blockEnd, overflow := new(uint256.Int).AddOverflow(blockStart, uint256.NewInt(span))
	if overflow || span > rangeLimit {
		return nil, errors.New("requested range exceeds bounds")
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
		return nil, err
	}
	defer closeStream(stream)

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
