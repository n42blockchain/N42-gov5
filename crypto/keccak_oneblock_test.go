package crypto

import (
	"math/rand"
	"testing"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/types"
)

func refKeccak(data []byte) (out [32]byte) {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	h.Sum(out[:0])
	return out
}

func TestKeccak256OneBlockEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for n := 0; n <= 300; n++ {
		for iter := 0; iter < 64; iter++ {
			buf := make([]byte, n)
			switch iter {
			case 0: // all zero
			case 1:
				for i := range buf {
					buf[i] = 0xff
				}
			default:
				rng.Read(buf)
			}
			want := refKeccak(buf)
			if got := Keccak256Hash(buf); got != want {
				t.Fatalf("Keccak256Hash len %d mismatch", n)
			}
			if got := Keccak256HashOne(buf); got != want {
				t.Fatalf("Keccak256HashOne len %d mismatch", n)
			}
			if n < keccak256Rate {
				if got, ok := Keccak256OneBlock(buf); !ok || got != want {
					t.Fatalf("Keccak256OneBlock len %d mismatch", n)
				}
			} else if _, ok := Keccak256OneBlock(buf); ok {
				t.Fatalf("Keccak256OneBlock accepted len %d", n)
			}
		}
	}
	// Known vector: keccak256("") .
	if Keccak256HashOne(nil).Hex() != "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470" {
		t.Fatal("empty-input vector mismatch")
	}
}

func FuzzKeccak256OneBlock(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 135))
	f.Add(make([]byte, 136))
	f.Fuzz(func(t *testing.T, data []byte) {
		if Keccak256HashOne(data) != refKeccak(data) {
			t.Fatalf("mismatch len %d", len(data))
		}
	})
}

func benchInputs(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(int64(n))).Read(b)
	return b
}

func BenchmarkKeccakSizes(b *testing.B) {
	for _, n := range []int{64, 70, 110} {
		data := benchInputs(n)
		b.Run("pooled/"+itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = Keccak256Hash(data)
			}
		})
		b.Run("oneblock/"+itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = Keccak256OneBlock(data)
			}
		})
	}
}

func itoa(n int) string {
	return string([]byte{byte('0' + n/100%10), byte('0' + n/10%10), byte('0' + n%10)})
}

func TestKeccak256x4OneBlockEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for iter := 0; iter < 20000; iter++ {
		var in [4][]byte
		for i := range in {
			n := rng.Intn(301)
			if iter%2 == 0 {
				n = rng.Intn(keccak256Rate)
			}
			if iter%7 == 0 {
				n = 135 + rng.Intn(3) // around the block boundary
			}
			in[i] = make([]byte, n)
			rng.Read(in[i])
		}
		var out [4]types.Hash
		short := true
		for i := range in {
			short = short && len(in[i]) < keccak256Rate
		}
		if ok := Keccak256x4OneBlock(&in, &out); ok != short {
			t.Fatalf("iter %d: ok %v, all short %v", iter, ok, short)
		}
		if !short {
			continue
		}
		for i := range in {
			if out[i] != refKeccak(in[i]) {
				t.Fatalf("iter %d lane %d len %d mismatch", iter, i, len(in[i]))
			}
		}
	}
}

func BenchmarkKeccakX4(b *testing.B) {
	var in [4][]byte
	for i := range in {
		in[i] = benchInputs(110 - i)
	}
	var out [4]types.Hash
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Keccak256x4OneBlock(&in, &out)
	}
}
