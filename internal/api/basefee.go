package api

import (
	"errors"
	"math/big"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
)

func uint256ToBigOrNil(v *uint256.Int) *big.Int {
	if v == nil {
		return nil
	}
	return v.ToBig()
}

func uint256ToBigOrZero(v *uint256.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v.ToBig()
}

func uint256ToUint64OrZero(v *uint256.Int) uint64 {
	if v == nil {
		return 0
	}
	return v.Uint64()
}

func requireUint256(v *uint256.Int, msg string) (*uint256.Int, error) {
	if v == nil {
		return nil, errors.New(msg)
	}
	return v, nil
}

func uint256FromBig(v *big.Int) (*uint256.Int, error) {
	if v == nil {
		return nil, nil
	}
	if v.Sign() < 0 {
		return nil, errors.New("value must be non-negative")
	}
	out := new(uint256.Int)
	if out.SetFromBig(v) {
		return nil, errors.New("value overflows uint256")
	}
	return out, nil
}

func headerBaseFeeBig(header block.IHeader) *big.Int {
	if header == nil {
		return nil
	}
	return uint256ToBigOrNil(header.BaseFee64())
}
