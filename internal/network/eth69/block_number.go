package eth69

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
)

func requireBlockNumber(blk *block.Block, msg string) (*uint256.Int, error) {
	if blk == nil {
		return nil, errors.New("block is nil")
	}
	number := blk.Number64()
	if number == nil {
		return nil, errors.New(msg)
	}
	return number, nil
}
