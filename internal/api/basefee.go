package api

import (
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

func headerBaseFeeBig(header block.IHeader) *big.Int {
	if header == nil {
		return nil
	}
	return uint256ToBigOrNil(header.BaseFee64())
}
