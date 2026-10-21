package keccakf1600

import (
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/crypto/sha3"
)

func randStates(rng *rand.Rand, n int) [][25]uint64 {
	s := make([][25]uint64, n)
	for i := range s {
		for j := range s[i] {
			s[i][j] = rng.Uint64()
		}
	}
	return s
}

func TestPermuteX8(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	for iter := 0; iter < 200; iter++ {
		in := randStates(rng, 8)
		if iter == 0 {
			in = make([][25]uint64, 8) // all-zero states
		}
		var st StateX8
		a := st.Initialize()
		if uintptr(len(a)) != 200 {
			t.Fatal("bad buffer length")
		}
		for i := 0; i < 8; i++ {
			for j := 0; j < 25; j++ {
				a[8*j+i] = in[i][j]
			}
		}
		st.Permute()
		for i := 0; i < 8; i++ {
			want := in[i]
			sha3.KeccakF1600(&want, false)
			for j := 0; j < 25; j++ {
				if a[8*j+i] != want[j] {
					t.Fatalf("iter %d state %d lane %d mismatch", iter, i, j)
				}
			}
		}
	}
}

// TestPermuteX8LanesIndependent changes one state and checks that the
// other seven outputs do not move.
func TestPermuteX8LanesIndependent(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	in := randStates(rng, 8)
	run := func(in [][25]uint64) (out [8][25]uint64) {
		var st StateX8
		a := st.Initialize()
		for i := 0; i < 8; i++ {
			for j := 0; j < 25; j++ {
				a[8*j+i] = in[i][j]
			}
		}
		st.Permute()
		for i := 0; i < 8; i++ {
			for j := 0; j < 25; j++ {
				out[i][j] = a[8*j+i]
			}
		}
		return out
	}
	base := run(in)
	for k := 0; k < 8; k++ {
		mod := append([][25]uint64(nil), in...)
		mod[k][rng.Intn(25)] ^= 1 << uint(rng.Intn(64))
		got := run(mod)
		for i := 0; i < 8; i++ {
			if (got[i] == base[i]) != (i != k) {
				t.Fatalf("flipping state %d affected state %d", k, i)
			}
		}
	}
}

func TestPermuteX8Scalar(t *testing.T) {
	rng := rand.New(rand.NewSource(10))
	in := randStates(rng, 8)
	var a, b [200]uint64
	for i := 0; i < 8; i++ {
		for j := 0; j < 25; j++ {
			a[8*j+i] = in[i][j]
		}
	}
	b = a
	permuteSIMDx8(a[:])
	permuteScalarX8(b[:])
	if a != b {
		t.Fatal("SIMD and scalar eight-way disagree")
	}
}

func TestPermuteX4VL(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for iter := 0; iter < 200; iter++ {
		in := randStates(rng, 4)
		var a, b [100]uint64
		for i := 0; i < 4; i++ {
			for j := 0; j < 25; j++ {
				a[4*j+i] = in[i][j]
			}
		}
		b = a
		permuteSIMDx4VL(a[:])
		permuteScalarX4(b[:], false)
		if a != b {
			t.Fatalf("iter %d: AVX-512VL four-way disagrees with scalar", iter)
		}
	}
}

func BenchmarkPermute(b *testing.B) {
	var st4 StateX4
	st4.Initialize(false)
	var st8 StateX8
	st8.Initialize()
	var one [25]uint64
	b.Run("scalar", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sha3.KeccakF1600(&one, false)
		}
	})
	b.Run("x4avx2", func(b *testing.B) {
		a := st4.a[st4.offset:]
		for i := 0; i < b.N; i++ {
			permuteSIMDx4(a, false)
		}
	})
	if IsEnabledX4VL() {
		b.Run("x4avx512vl", func(b *testing.B) {
			a := st4.a[st4.offset:]
			for i := 0; i < b.N; i++ {
				permuteSIMDx4VL(a)
			}
		})
	}
	b.Run("x8", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			st8.Permute()
		}
	})
}
