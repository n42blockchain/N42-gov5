package hexutil

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"
)

// TestEncodeBigMatchesFmt: EncodeBig produces exactly fmt's "%#x" text
// (and "0x0" for zero) for small, large and negative values.
func TestEncodeBigMatchesFmt(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	vals := []*big.Int{big.NewInt(1), big.NewInt(-1), big.NewInt(15), big.NewInt(16), big.NewInt(-255),
		new(big.Int).Lsh(big.NewInt(1), 255), new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 300))}
	for i := 0; i < 2000; i++ {
		v := new(big.Int).Rand(r, new(big.Int).Lsh(big.NewInt(1), uint(r.Intn(300)+1)))
		if i%3 == 0 {
			v.Neg(v)
		}
		vals = append(vals, v)
	}
	for _, v := range vals {
		want := "0x0"
		if v.BitLen() != 0 {
			want = fmt.Sprintf("%#x", v)
		}
		if got := EncodeBig(v); got != want {
			t.Fatalf("EncodeBig(%s) = %q, want %q", v, got, want)
		}
	}
}

func BenchmarkEncodeBig(b *testing.B) {
	v, _ := new(big.Int).SetString("123456789abcdef0123456789", 16)
	for i := 0; i < b.N; i++ {
		_ = EncodeBig(v)
	}
}

func BenchmarkEncodeBigFmt(b *testing.B) {
	v, _ := new(big.Int).SetString("123456789abcdef0123456789", 16)
	for i := 0; i < b.N; i++ {
		_ = fmt.Sprintf("%#x", v)
	}
}
