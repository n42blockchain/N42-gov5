package txspool

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

func currentBlock(chain common.IBlockChain) block.IBlock {
	if chain == nil {
		return nil
	}
	return chain.CurrentBlock()
}
