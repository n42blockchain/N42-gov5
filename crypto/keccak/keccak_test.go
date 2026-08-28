package keccak

import (
	"bytes"
	"math/rand"
	"testing"

	"golang.org/x/crypto/sha3"
)

// TestMatchesXCrypto: every input length across several rate boundaries,
// written in one piece and in random chunks, read as 32 bytes and as Sum,
// must equal x/crypto's legacy Keccak-256.
func TestMatchesXCrypto(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for n := 0; n <= 3*Rate+5; n++ {
		data := make([]byte, n)
		rng.Read(data)
		want := sha3.NewLegacyKeccak256()
		want.Write(data)
		ref := want.Sum(nil)
		if got := Sum256(data); !bytes.Equal(got[:], ref) {
			t.Fatalf("Sum256 len %d mismatch", n)
		}
		var s State
		for rest := data; len(rest) > 0; {
			c := 1 + rng.Intn(len(rest))
			s.Write(rest[:c])
			rest = rest[c:]
		}
		if got := s.Sum(nil); !bytes.Equal(got, ref) {
			t.Fatalf("chunked Sum len %d mismatch", n)
		}
		var out [32]byte
		s.Read(out[:])
		if !bytes.Equal(out[:], ref) {
			t.Fatalf("chunked Read len %d mismatch", n)
		}
		// Reset and reuse.
		s.Reset()
		s.Write(data)
		var again [32]byte
		s.Read(again[:])
		if again != out {
			t.Fatalf("reuse after Reset len %d mismatch", n)
		}
		// Long squeeze must match x/crypto's continued output.
		var long [100]byte
		s.Reset()
		s.Write(data)
		s.Read(long[:])
		xs := sha3.NewLegacyKeccak256()
		xs.Write(data)
		var xl [100]byte
		xs.(interface{ Read([]byte) (int, error) }).Read(xl[:])
		if long != xl {
			t.Fatalf("100-byte squeeze len %d mismatch", n)
		}
		var h [32]byte
		Sum256Into(&h, data[:n/2], data[n/2:])
		if !bytes.Equal(h[:], ref) {
			t.Fatalf("Sum256Into len %d mismatch", n)
		}
	}
}

func TestNoAllocs(t *testing.T) {
	data := make([]byte, 64)
	var sink [32]byte
	if a := testing.AllocsPerRun(1000, func() { sink = Sum256(data) }); a != 0 {
		t.Fatalf("Sum256 allocs = %v", a)
	}
	_ = sink
}

func BenchmarkSum256(b *testing.B) {
	for _, n := range []int{20, 32, 64, 136, 1024} {
		data := make([]byte, n)
		b.Run(string(rune('a'+n%26)), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(n))
			var h [32]byte
			for i := 0; i < b.N; i++ {
				h = Sum256(data)
			}
			_ = h
		})
	}
}
