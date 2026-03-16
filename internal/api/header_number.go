package api

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
)

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
