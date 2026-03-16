package sync

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
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

func currentBlockNumber(chain common.IBlockChain) *uint256.Int {
	number, err := requireCurrentBlockNumber(chain, "")
	if err != nil {
		return uint256.NewInt(0)
	}
	return number.Clone()
}

func requireHeaderNumber(header block.IHeader, msg string) (*uint256.Int, error) {
	if header == nil {
		return nil, errors.New("header is nil")
	}
	number := header.Number64()
	if number == nil {
		return nil, errors.New(msg)
	}
	return number, nil
}

func blockNumberOrZero(blk block.IBlock) uint64 {
	number, err := requireBlockNumber(blk, "")
	if err != nil {
		return 0
	}
	return number.Uint64()
}

func currentBlockNumberOrZero(chain common.IBlockChain) uint64 {
	return currentBlockNumber(chain).Uint64()
}

func headerNumberOrZero(header block.IHeader) uint64 {
	number, err := requireHeaderNumber(header, "")
	if err != nil {
		return 0
	}
	return number.Uint64()
}
