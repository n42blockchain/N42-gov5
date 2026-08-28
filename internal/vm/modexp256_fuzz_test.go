package vm

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/n42blockchain/N42/common/avmutil"
)

// refModexp is the math/big path the precompile used for every input before
// the field fast path existed, restricted to the 32/32/32 shape.
func refModexp(base, exp, mod []byte) []byte {
	b := new(big.Int).SetBytes(base)
	e := new(big.Int).SetBytes(exp)
	m := new(big.Int).SetBytes(mod)
	var v []byte
	switch {
	case m.BitLen() == 0:
		return avmutil.LeftPadBytes([]byte{}, 32)
	case b.Cmp(big.NewInt(1)) == 0:
		v = b.Mod(b, m).Bytes()
	default:
		v = new(big.Int).Exp(b, e, m).Bytes()
	}
	return avmutil.LeftPadBytes(v, 32)
}

func modexpModuli() [][32]byte {
	one := big.NewInt(1)
	var out [][32]byte
	for _, q := range []*big.Int{fr.Modulus(), fp.Modulus()} {
		for _, d := range []int64{0, -1, 1, -2, 2} {
			var m [32]byte
			new(big.Int).Add(q, big.NewInt(d)).FillBytes(m[:])
			out = append(out, m)
		}
	}
	var m [32]byte
	new(big.Int).Sub(new(big.Int).Lsh(one, 256), one).FillBytes(m[:])
	out = append(out, m)
	return out
}

// FuzzModexp256 drives the precompile's public Run with the 32/32/32 shape,
// choosing the modulus among the BN254 fields, their neighbours and the
// fuzzer's own bytes, and requires the math/big result byte for byte.
func FuzzModexp256(f *testing.F) {
	one := big.NewInt(1)
	max256 := new(big.Int).Sub(new(big.Int).Lsh(one, 256), one)
	var seeds [][]byte
	for _, q := range []*big.Int{fr.Modulus(), fp.Modulus()} {
		for _, b := range []*big.Int{big.NewInt(0), one, big.NewInt(2), new(big.Int).Sub(q, one), q, new(big.Int).Add(q, one), max256} {
			for _, e := range []*big.Int{big.NewInt(0), one, new(big.Int).Sub(q, big.NewInt(2)), new(big.Int).Sub(q, one), q, max256} {
				var bb, eb [32]byte
				b.FillBytes(bb[:])
				e.FillBytes(eb[:])
				seeds = append(seeds, append(append([]byte{}, bb[:]...), eb[:]...))
			}
		}
	}
	for _, s := range seeds {
		f.Add(s[:32], s[32:], byte(0), s[:32])
	}
	moduli := modexpModuli()
	f.Fuzz(func(t *testing.T, base, exp []byte, sel byte, modRaw []byte) {
		if len(base) != 32 || len(exp) != 32 {
			return
		}
		var mod [32]byte
		if int(sel) < len(moduli) {
			mod = moduli[sel]
		} else {
			copy(mod[:], modRaw)
		}
		var in [192]byte
		in[31], in[63], in[95] = 32, 32, 32
		copy(in[96:128], base)
		copy(in[128:160], exp)
		copy(in[160:192], mod[:])
		got, err := (&bigModExp{}).Run(in[:])
		if err != nil {
			t.Fatal(err)
		}
		want := refModexp(base, exp, mod[:])
		if !bytes.Equal(got, want) {
			t.Fatalf("base=%x exp=%x mod=%x: got %x want %x", base, exp, mod, got, want)
		}
		// direct fast-path probe: must accept exactly the two field moduli
		_, ok := modexp256(in[96:])
		isField := bytes.Equal(mod[:], bn254FrMod[:]) || bytes.Equal(mod[:], bn254FpMod[:])
		if ok != isField {
			t.Fatalf("mod=%x: fast path ok=%v isField=%v", mod, ok, isField)
		}
	})
}
