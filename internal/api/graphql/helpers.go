package graphql

import "github.com/holiman/uint256"

func uint256ToUint64OrZero(v *uint256.Int) uint64 {
	if v == nil {
		return 0
	}
	return v.Uint64()
}
