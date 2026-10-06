// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// The appended EIP-155 signing payload hashes to exactly what the reflective
// RLP encode produced, across integer and length boundaries.
func TestLegacySigningHashMatchesRlpHash(t *testing.T) {
	to := types.Address{0xde, 0xad}
	big1 := uint256.MustFromDecimal("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	for _, chainID := range []*big.Int{big.NewInt(1), big.NewInt(94), big.NewInt(127), big.NewInt(128), new(big.Int).Lsh(big.NewInt(1), 64)} {
		for _, nonce := range []uint64{0, 1, 127, 128, 1 << 40, ^uint64(0)} {
			for _, dl := range []int{0, 1, 55, 56, 1000, 70000} {
				data := make([]byte, dl)
				if dl == 1 {
					data[0] = 0x7f
				}
				for _, dest := range []*types.Address{&to, nil} {
					for _, v := range []*uint256.Int{nil, uint256.NewInt(0), uint256.NewInt(127), uint256.NewInt(128), big1} {
						tx := NewTx(&LegacyTx{Nonce: nonce, GasPrice: v, Gas: nonce >> 3, To: dest, Value: v, Data: data})
						want := hash.RlpHash([]interface{}{
							tx.Nonce(), tx.GasPrice(), tx.Gas(), tx.To(), tx.Value(), tx.Data(),
							chainID, uint(0), uint(0),
						})
						if got := legacySigningHash(tx, chainID); got != want {
							t.Fatalf("chain %s nonce %d data %d to-nil %v value %v: %x != %x", chainID, nonce, dl, dest == nil, v, got, want)
						}
					}
				}
			}
		}
	}
}
