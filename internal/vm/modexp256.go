package vm

import (
	"bytes"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// Fast path for the modexp precompile's dominant shape. A census of mainnet
// blocks 24.98M–25.0M found 99.98% of modexp calls are 32/32/32-byte, and
// 97% of those are an Fr inversion (mod = BN254 r, exp = r-2) issued by
// Groth16 verifiers. math/big spends ~50µs on one; gnark's fixed-field
// arithmetic does the inversion in ~1µs and a general exponent in a few.
var (
	bn254FrMod, bn254FrInvExp = fieldConstants(fr.Modulus())
	bn254FpMod, bn254FpInvExp = fieldConstants(fp.Modulus())
)

func fieldConstants(q *big.Int) (mod, invExp [32]byte) {
	q.FillBytes(mod[:])
	new(big.Int).Sub(q, big.NewInt(2)).FillBytes(invExp[:])
	return
}

// modexp256 computes base^exp mod m for 32-byte operands when m is the BN254
// scalar or base field modulus. in is the operand area (base || exp || mod),
// at least 96 bytes. Returns ok=false for any other modulus.
func modexp256(in []byte) ([]byte, bool) {
	if len(in) < 96 {
		return nil, false
	}
	base, exp, mod := in[:32], in[32:64], in[64:96]
	switch {
	case bytes.Equal(mod, bn254FrMod[:]):
		var x fr.Element
		x.SetBytes(base) // reduces mod r, as big.Int.Exp would
		if bytes.Equal(exp, bn254FrInvExp[:]) {
			x.Inverse(&x) // Inverse(0) = 0 = 0^(r-2) mod r
		} else {
			x.Exp(x, new(big.Int).SetBytes(exp))
		}
		out := x.Bytes()
		return out[:], true
	case bytes.Equal(mod, bn254FpMod[:]):
		var x fp.Element
		x.SetBytes(base)
		if bytes.Equal(exp, bn254FpInvExp[:]) {
			x.Inverse(&x)
		} else {
			x.Exp(x, new(big.Int).SetBytes(exp))
		}
		out := x.Bytes()
		return out[:], true
	}
	return nil, false
}
