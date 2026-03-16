package internal

import (
	"errors"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
)

func requireConcreteBlock(blk block.IBlock, msg string) (*block.Block, error) {
	concreteBlock, ok := blk.(*block.Block)
	if !ok || concreteBlock == nil {
		return nil, errors.New(msg)
	}
	return concreteBlock, nil
}

func requireConcreteHeader(header block.IHeader, msg string) (*block.Header, error) {
	concreteHeader, ok := header.(*block.Header)
	if !ok || concreteHeader == nil {
		return nil, errors.New(msg)
	}
	return concreteHeader, nil
}

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
