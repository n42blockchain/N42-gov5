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

func TestKeccak256x8OneBlockEquivalence(t *testing.T) {
	t.Run("gather", testKeccak256x8OneBlock)
	t.Run("state", func(t *testing.T) {
		keccakX8Gather = false
		defer func() { keccakX8Gather = true }()
		testKeccak256x8OneBlock(t)
	})
}

func testKeccak256x8OneBlock(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	check := func(iter int, in *[8][]byte) {
		var out [8]types.Hash
		short := true
		for i := range in {
			short = short && len(in[i]) < keccak256Rate
		}
		if ok := Keccak256x8OneBlock(in, &out); ok != short {
			t.Fatalf("iter %d: ok %v, all short %v", iter, ok, short)
		}
		if !short {
			return
		}
		for i := range in {
			if out[i] != refKeccak(in[i]) {
				t.Fatalf("iter %d lane %d len %d mismatch", iter, i, len(in[i]))
			}
		}
	}
	// Every length 0..135 in every lane, with distinct lengths per lane;
	// odd lanes carry spare capacity full of garbage past len.
	for n := 0; n < keccak256Rate; n++ {
		var in [8][]byte
		for i := range in {
			l := (n + 17*i) % keccak256Rate
			in[i] = make([]byte, l, l+(i&1)*(1+rng.Intn(16)))
			rng.Read(in[i][:cap(in[i])])
		}
		check(n, &in)
	}
	for iter := 0; iter < 20000; iter++ {
		var in [8][]byte
		for i := range in {
			n := rng.Intn(keccak256Rate)
			if iter%5 == 0 {
				n = rng.Intn(301)
			}
			if iter%7 == 0 {
				n = 135 + rng.Intn(3)
			}
			in[i] = make([]byte, n, n+rng.Intn(3)*rng.Intn(9))
			rng.Read(in[i][:cap(in[i])])
			if iter%11 == 0 {
				for k := range in[i] {
					in[i][k] = 0xff
				}
			} else {
				rng.Read(in[i])
			}
		}
		check(iter, &in)
	}
}

func FuzzKeccak256x8OneBlock(f *testing.F) {
	f.Add([]byte{}, uint64(0))
	f.Add(make([]byte, 135), uint64(0x0102030405060708))
	f.Fuzz(func(t *testing.T, data []byte, cuts uint64) {
		// Eight lanes cut from data at offsets/lengths derived from cuts.
		var in [8][]byte
		for i := range in {
			c := int(cuts >> (8 * i) & 0xff)
			lo := 0
			if len(data) > 0 {
				lo = c % (len(data) + 1)
			}
			hi := lo + (c*7+i)%keccak256Rate
			if hi > len(data) {
				hi = len(data)
			}
			in[i] = data[lo:hi]
		}
		var out [8]types.Hash
		if !Keccak256x8OneBlock(&in, &out) {
			t.Fatal("rejected short inputs")
		}
		for i := range in {
			if out[i] != refKeccak(in[i]) {
				t.Fatalf("lane %d len %d mismatch", i, len(in[i]))
			}
		}
	})
}

// BenchmarkKeccakPerHash reports ns per hash of ~110-byte inputs for each
// path.
func BenchmarkKeccakPerHash(b *testing.B) {
	var in [8][]byte
	for i := range in {
		in[i] = benchInputs(110 - i)
	}
	b.Run("scalar", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = Keccak256OneBlock(in[i&7])
		}
	})
	b.Run("x4", func(b *testing.B) {
		var in4 [4][]byte
		copy(in4[:], in[:4])
		var out [4]types.Hash
		for i := 0; i < b.N; i += 4 {
			Keccak256x4OneBlock(&in4, &out)
		}
	})
	b.Run("x8", func(b *testing.B) {
		var out [8]types.Hash
		for i := 0; i < b.N; i += 8 {
			Keccak256x8OneBlock(&in, &out)
		}
	})
}
