package filters

import (
	"errors"

	"github.com/n42blockchain/N42/common/block"
)

func requireHeaderNumber(header block.IHeader, msg string) (uint64, error) {
	if header == nil {
		return 0, errors.New("header is nil")
	}
	number := header.Number64()
	if number == nil {
		return 0, errors.New(msg)
	}
	return number.Uint64(), nil
}
