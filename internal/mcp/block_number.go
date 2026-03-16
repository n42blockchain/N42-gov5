package mcp

import "github.com/n42blockchain/N42/common/block"

func blockNumberOrZero(blk block.IBlock) uint64 {
	if blk == nil {
		return 0
	}
	number := blk.Number64()
	if number == nil {
		return 0
	}
	return number.Uint64()
}
