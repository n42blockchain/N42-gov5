package vm

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// TestModexp256MatchesBigInt: the field fast path must agree with math/big
// on every operand shape the precompile can see, including unreduced bases,
// zero, one, and exponents around the modulus.
func TestModexp256MatchesBigInt(t *testing.T) {
	one := big.NewInt(1)
	max256 := new(big.Int).Sub(new(big.Int).Lsh(one, 256), one)
	for _, q := range []*big.Int{fr.Modulus(), fp.Modulus()} {
		edge := []*big.Int{big.NewInt(0), one, big.NewInt(2), new(big.Int).Sub(q, big.NewInt(2)),
			new(big.Int).Sub(q, one), q, new(big.Int).Add(q, one), max256}
		bases := append([]*big.Int{}, edge...)
		exps := append([]*big.Int{}, edge...)
		for i := 0; i < 24; i++ {
			b, _ := rand.Int(rand.Reader, max256)
			e, _ := rand.Int(rand.Reader, max256)
			bases = append(bases, b)
			exps = append(exps, e)
		}
		var in [96]byte
		q.FillBytes(in[64:96])
		for _, b := range bases {
			for _, e := range exps {
				b.FillBytes(in[0:32])
				e.FillBytes(in[32:64])
				got, ok := modexp256(in[:])
				if !ok {
					t.Fatal("fast path did not recognise the field modulus")
				}
				want := make([]byte, 32)
				new(big.Int).Exp(b, e, q).FillBytes(want)
				if string(got) != string(want) {
					t.Fatalf("mod=%s base=%s exp=%s: got %x want %x", q, b, e, got, want)
				}
			}
		}
	}
	var other [96]byte
	other[95] = 7
	if _, ok := modexp256(other[:]); ok {
		t.Fatal("fast path accepted a non-field modulus")
	}
	if _, ok := modexp256(other[:64]); ok {
		t.Fatal("fast path accepted a short input")
	}
}

// TestBigModExpUsesFastPath: the precompile must return the field result
// through its public entry point for the 32/32/32 Fr-inversion shape.
func TestBigModExpUsesFastPath(t *testing.T) {
	var in [192]byte
	in[31], in[63], in[95] = 32, 32, 32
	x := big.NewInt(123456789)
	x.FillBytes(in[96:128])
	new(big.Int).Sub(fr.Modulus(), big.NewInt(2)).FillBytes(in[128:160])
	fr.Modulus().FillBytes(in[160:192])
	got, err := (&bigModExp{}).Run(in[:])
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 32)
	new(big.Int).ModInverse(x, fr.Modulus()).FillBytes(want)
	if string(got) != string(want) {
		t.Fatalf("got %x want %x", got, want)
	}
}
