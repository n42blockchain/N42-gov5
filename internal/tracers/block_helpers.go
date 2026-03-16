package tracers

import (
	"errors"
	"math/big"

	"github.com/holiman/uint256"
	types "github.com/n42blockchain/N42/common/block"
)

func requireBlockNumber(block *types.Block, msg string) (*uint256.Int, error) {
	if block == nil {
		return nil, errors.New("block is nil")
	}
	number := block.Number64()
	if number == nil {
		return nil, errors.New(msg)
	}
	return number, nil
}

func blockBaseFeeBig(block *types.Block) *big.Int {
	if block == nil {
		return nil
	}
	baseFee := block.BaseFee64()
	if baseFee == nil {
		return nil
	}
	return baseFee.ToBig()
}
