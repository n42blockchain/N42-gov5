package vm

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/n42blockchain/N42/crypto/bn256"
)

// bn256Recipe turns fuzzer bytes into either raw precompile input or a
// structured input built from valid curve points with selected coordinates
// overwritten by boundary values (p, p+1, 2^256-1, zero).
func bn256Recipe(data []byte) (op byte, input []byte) {
	if len(data) < 2 {
		return 0, nil
	}
	op, mode := data[0]%3, data[1]
	data = data[2:]
	if mode&1 == 0 {
		// raw bytes: exactly the wire size for the op, or whatever is left
		size := map[byte]int{0: 128, 1: 96, 2: 192 * (1 + int(mode>>1)%4)}[op]
		if mode>>1 == 0 || len(data) < size {
			return op, data
		}
		return op, data[:size]
	}
	var pBytes, p1Bytes, maxBytes [32]byte
	fp.Modulus().FillBytes(pBytes[:])
	new(big.Int).Add(fp.Modulus(), big.NewInt(1)).FillBytes(p1Bytes[:])
	for i := range maxBytes {
		maxBytes[i] = 0xff
	}
	next := func(n int) []byte {
		if len(data) < n {
			out := make([]byte, n)
			copy(out, data)
			data = nil
			return out
		}
		out := data[:n]
		data = data[n:]
		return out
	}
	scalar := func() *big.Int { return new(big.Int).Mod(new(big.Int).SetBytes(next(32)), fr.Modulus()) }
	g1 := func() []byte {
		k := scalar()
		if k.Sign() == 0 {
			return make([]byte, 64)
		}
		return new(bn256.G1).ScalarBaseMult(k).Marshal()
	}
	g2 := func() []byte {
		k := scalar()
		if k.Sign() == 0 {
			return make([]byte, 128)
		}
		return new(bn256.G2).ScalarBaseMult(k).Marshal()
	}
	tweak := func(b []byte) []byte {
		sel := next(1)[0]
		word := int(sel>>3) % (len(b) / 32)
		switch sel & 7 {
		case 1:
			copy(b[word*32:], pBytes[:])
		case 2:
			copy(b[word*32:], p1Bytes[:])
		case 3:
			copy(b[word*32:], maxBytes[:])
		case 4:
			copy(b[word*32:], make([]byte, 32))
		case 5:
			b[word*32+31] ^= 1
		case 6: // negate y of a G1 point: still on curve
			if len(b) == 64 {
				y := new(big.Int).SetBytes(b[32:])
				if y.Sign() != 0 {
					y.Sub(fp.Modulus(), y)
					y.FillBytes(b[32:])
				}
			}
		}
		return b
	}
	switch op {
	case 0:
		return op, append(tweak(g1()), tweak(g1())...)
	case 1:
		return op, append(tweak(g1()), next(32)...)
	default:
		n := 1 + int(next(1)[0]%5)
		var in []byte
		for i := 0; i < n; i++ {
			in = append(in, tweak(g1())...)
			in = append(in, tweak(g2())...)
		}
		if mode&2 != 0 { // make the product trivially 1: e(P,Q)·e(-P,Q)
			p := g1()
			q := g2()
			neg := append([]byte{}, p...)
			y := new(big.Int).SetBytes(neg[32:])
			if y.Sign() != 0 {
				y.Sub(fp.Modulus(), y)
				y.FillBytes(neg[32:])
			}
			in = append(in, p...)
			in = append(in, q...)
			in = append(in, make([]byte, 192)...) // infinity pair in the middle
			in = append(in, neg...)
			in = append(in, q...)
		}
		return op, in
	}
}

// FuzzBn256Precompiles compares the gnark-backed ecAdd/ecMul/ecPairing with
// the cloudflare implementation on raw and structured inputs.
func FuzzBn256Precompiles(f *testing.F) {
	f.Add([]byte{0, 1, 1, 2, 3})
	f.Add([]byte{1, 1, 5, 6, 7})
	f.Add([]byte{2, 3, 9, 8, 7, 6})
	f.Add([]byte{2, 1, 0, 0, 0})
	f.Add(append([]byte{0, 0}, make([]byte, 128)...))
	f.Add(append([]byte{2, 0}, make([]byte, 192)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		op, in := bn256Recipe(data)
		var gnark, cf func([]byte) ([]byte, error)
		switch op {
		case 0:
			gnark, cf = runBn256Add, cfBn256Add
		case 1:
			gnark, cf = runBn256ScalarMul, cfBn256ScalarMul
		default:
			gnark, cf = runBn256Pairing, cfBn256Pairing
		}
		outG, errG := gnark(in)
		outC, errC := cf(in)
		if (errG == nil) != (errC == nil) || !bytes.Equal(outG, outC) {
			t.Fatalf("op %d input %x:\n cloudflare (%x, %v)\n gnark      (%x, %v)", op, in, outC, errC, outG, errG)
		}
	})
}
