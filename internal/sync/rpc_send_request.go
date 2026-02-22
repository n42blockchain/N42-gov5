package sync

import (
	"context"
	"io"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/api/protocol/sync_pb"
	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/utils"
)

// ErrInvalidFetchedData is thrown if stream fails to provide requested blocks.
var ErrInvalidFetchedData = errors.New("invalid data returned from peer")

// BlockProcessor defines a block processing function, which allows to start utilizing
// blocks even before all blocks are ready.
type BlockProcessor func(block *types_pb.Block) error

// SendBodiesByRangeRequest sends BeaconBlocksByRange and returns fetched blocks, if any.
func SendBodiesByRangeRequest(ctx context.Context, chain common.IBlockChain, p2pProvider p2p.SenderEncoder, pid peer.ID, req *sync_pb.BodiesByRangeRequest, blockProcessor BlockProcessor) ([]*types_pb.Block, error) {
	if req.Step == 0 {
		return nil, errors.New("request step cannot be zero")
	}
	if req.Count == 0 {
		return nil, errors.New("request count cannot be zero")
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

	blocks := make([]*types_pb.Block, 0, req.Count)
	process := func(blk *types_pb.Block) error {
		blocks = append(blocks, blk)
		if blockProcessor != nil {
			return blockProcessor(blk)
		}
		return nil
	}

	var prevBlockNr *uint256.Int
	blockStart := utils.ConvertH256ToUint256Int(req.StartBlockNumber)
	blockEnd := new(uint256.Int).AddUint64(blockStart, req.Count*req.Step)

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

		blockNr := utils.ConvertH256ToUint256Int(blk.Header.Number)

		// Returned blocks MUST be in the slot range [start_slot, start_slot + count * step).
		if blockNr.Cmp(blockStart) < 0 || blockNr.Cmp(blockEnd) >= 0 {
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
