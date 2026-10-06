// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hash

import (
	"encoding/hex"
	"fmt"
	"runtime"
	"testing"

	"lukechampine.com/blake3"

	"github.com/n42blockchain/N42/common/types"
)

// serialBlake3Root is the specification written the slow way.
func serialBlake3Root(l rawList) types.Hash {
	if len(l) == 0 {
		return types.Hash(blake3.Sum256(nil))
	}
	var level []types.Hash
	for _, v := range l {
		level = append(level, blake3.Sum256(append([]byte{0x00}, v...)))
	}
	for len(level) > 1 {
		var next []types.Hash
		for i := 0; i+1 < len(level); i += 2 {
			m := append([]byte{0x01}, level[i][:]...)
			m = append(m, level[i+1][:]...)
			next = append(next, blake3.Sum256(m))
		}
		if len(level)%2 == 1 {
			next = append(next, level[len(level)-1])
		}
		level = next
	}
	return level[0]
}

func TestBlake3BinaryRootMatchesSpec(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4, 5, 7, 8, 9, 1023, 1024, 1025, 4095, 4097, 65537, 163000} {
		l := pseudoList(n, 1, 120, uint64(n)+11)
		want := serialBlake3Root(l)
		got := Blake3BinaryRoot(l)
		if got != want {
			t.Fatalf("n=%d: %x != spec %x", n, got[:8], want[:8])
		}
	}
	if Blake3BinaryRoot(rawList(nil)) != EmptyBlake3Root {
		t.Fatal("empty root")
	}
	// The root is independent of the worker count.
	l := pseudoList(163000, 100, 120, 3)
	first := Blake3BinaryRoot(l)
	prev := runtime.GOMAXPROCS(1)
	one := Blake3BinaryRoot(l)
	runtime.GOMAXPROCS(prev)
	if one != first {
		t.Fatalf("root depends on GOMAXPROCS: %x / %x", first[:8], one[:8])
	}
}

// Fixed vectors for the cross-client suite: the empty list, one entry, and
// the three-entry case that exercises the odd promotion.
func TestBlake3BinaryRootVectors(t *testing.T) {
	vectors := []struct {
		name string
		list rawList
		want string
	}{
		{"empty", nil, "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"},
		{"one", rawList{{0x01, 0x02, 0x03}}, ""},
		{"three", rawList{{0x01}, {0x02}, {0x03}}, ""},
	}
	for _, v := range vectors {
		got := Blake3BinaryRoot(v.list)
		if v.want == "" {
			// Print once so the vector can be copied to the Rust suite.
			t.Logf("vector %s: %s", v.name, hex.EncodeToString(got[:]))
			if got != serialBlake3Root(v.list) {
				t.Fatalf("vector %s disagrees with the spec", v.name)
			}
			continue
		}
		if hex.EncodeToString(got[:]) != v.want {
			t.Fatalf("vector %s: %x, want %s", v.name, got, v.want)
		}
	}
}

func pseudoList(n int, minLen, maxLen int, seed uint64) rawList {
	rng := seed
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }
	l := make(rawList, n)
	for i := range l {
		ln := minLen
		if maxLen > minLen {
			ln += int(next() % uint64(maxLen-minLen+1))
		}
		v := make([]byte, ln)
		for j := range v {
			v[j] = byte(next())
		}
		l[i] = v
	}
	return l
}

func BenchmarkTxRoot163k(b *testing.B) {
	l := pseudoList(163000, 110, 110, 1)
	for _, name := range []string{"erigon-mpt", "blake3-binary"} {
		b.Run(fmt.Sprintf("%s/txs=163000", name), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if name == "erigon-mpt" {
					DeriveShaErigon(l)
				} else {
					Blake3BinaryRoot(l)
				}
			}
		})
	}
}
