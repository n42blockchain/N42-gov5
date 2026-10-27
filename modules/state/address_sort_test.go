// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"math/rand"
	"slices"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestSortAddressesMatchesBytesCompare: the radix path and the small path
// produce exactly bytes.Compare order, for random sets, sets crowded into
// one 16-bit prefix (the fallback bucket), sets with duplicates, and sizes
// around the threshold.
func TestSortAddressesMatchesBytesCompare(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	gen := func(n int, prefix bool, dups bool) []types.Address {
		a := make([]types.Address, n)
		for i := range a {
			r.Read(a[i][:])
			if prefix && i%3 != 0 {
				a[i][0], a[i][1] = 0xAB, 0xCD
			}
			if dups && i > 0 && i%5 == 0 {
				a[i] = a[i-1]
			}
		}
		return a
	}
	for _, n := range []int{0, 1, 2, 100, radixSortMin - 1, radixSortMin, radixSortMin + 1, 31000, 100000} {
		for _, prefix := range []bool{false, true} {
			for _, dups := range []bool{false, true} {
				a := gen(n, prefix, dups)
				want := slices.Clone(a)
				slices.SortFunc(want, func(x, y types.Address) int { return bytes.Compare(x[:], y[:]) })
				sortAddresses(a)
				if !slices.Equal(a, want) {
					t.Fatalf("n=%d prefix=%v dups=%v: order differs from bytes.Compare", n, prefix, dups)
				}
			}
		}
	}
}

func BenchmarkSortAddresses31k(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	src := make([]types.Address, 31000)
	for i := range src {
		r.Read(src[i][:])
	}
	buf := make([]types.Address, len(src))
	b.Run("radix", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			copy(buf, src)
			sortAddresses(buf)
		}
	})
	b.Run("bytesCompare", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			copy(buf, src)
			slices.SortFunc(buf, func(x, y types.Address) int { return bytes.Compare(x[:], y[:]) })
		}
	})
}

// TestSortAddressesWithKeepsPairs: sortAddressesWith orders the addresses
// as sortAddresses does and carries every value with its address, for
// random sets and sets crowded into one 16-bit prefix, around the radix
// threshold.
func TestSortAddressesWithKeepsPairs(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for _, n := range []int{0, 1, 17, radixSortMin - 1, radixSortMin, radixSortMin + 1, 9000} {
		for _, prefix := range []bool{false, true} {
			addrs := make([]types.Address, n)
			vals := make([]int, n)
			want := map[types.Address]int{}
			for i := range addrs {
				r.Read(addrs[i][:])
				if prefix && i%3 != 0 {
					addrs[i][0], addrs[i][1] = 0xAB, 0xCD
				}
				vals[i] = i
				want[addrs[i]] = i
			}
			ref := slices.Clone(addrs)
			sortAddresses(ref)
			sortAddressesWith(addrs, vals)
			if !slices.Equal(ref, addrs) {
				t.Fatalf("n=%d prefix=%v: order differs from sortAddresses", n, prefix)
			}
			for i, a := range addrs {
				if want[a] != vals[i] {
					t.Fatalf("n=%d prefix=%v: value of %x moved off its address", n, prefix, a)
				}
			}
		}
	}
}
