package download

import (
	"errors"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/proto/types_pb"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/utils"
)

func requireBlockNumber(blk block.IBlock, msg string) (*uint256.Int, error) {
	if blk == nil {
		return nil, errors.New("block is nil")
	}
	number := blk.Number64()
	if number == nil {
		return nil, errors.New(msg)
	}
	return number, nil
}

func requireCurrentBlockNumber(chain common.IBlockChain, msg string) (*uint256.Int, error) {
	if chain == nil {
		return nil, errors.New("chain is nil")
	}
	return requireBlockNumber(chain.CurrentBlock(), msg)
}

func currentBlockNumberOrZero(chain common.IBlockChain) uint64 {
	number, err := requireCurrentBlockNumber(chain, "")
	if err != nil {
		return 0
	}
	return number.Uint64()
}

func cloneCurrentBlockNumberOrZero(chain common.IBlockChain) *uint256.Int {
	number, err := requireCurrentBlockNumber(chain, "")
	if err != nil {
		return uint256.NewInt(0)
	}
	return number.Clone()
}

func requireProtoHeaderNumber(header *types_pb.Header, msg string) (*uint256.Int, error) {
	if header == nil {
		return nil, errors.New("header is nil")
	}
	if header.Number == nil {
		return nil, errors.New(msg)
	}
	return utils.ConvertH256ToUint256Int(header.Number), nil
}

func requireProtoBlockNumber(blk *types_pb.Block, msg string) (*uint256.Int, error) {
	if blk == nil {
		return nil, errors.New("block is nil")
	}
	return requireProtoHeaderNumber(blk.Header, msg)
}
