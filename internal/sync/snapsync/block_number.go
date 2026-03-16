package snapsync

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
)

func currentBlockNumber(chain common.IBlockChain) *uint256.Int {
	if chain == nil {
		return uint256.NewInt(0)
	}
	current := chain.CurrentBlock()
	if current == nil || current.Number64() == nil {
		return uint256.NewInt(0)
	}
	return current.Number64()
}

func currentBlockNumberOrZero(chain common.IBlockChain) uint64 {
	return currentBlockNumber(chain).Uint64()
}
