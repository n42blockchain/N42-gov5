// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"encoding/binary"
	"slices"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// radixSortMin is the size from which sortAddresses distributes by the top
// 16 bits first. Below it a comparison sort is cheaper than the bucket
// table.
const radixSortMin = 2048

// radixBucketMax is the largest bucket insertion-sorted in place; a larger
// one (addresses sharing a 16-bit prefix far beyond chance) falls back to
// the comparison sort so a crafted set cannot make it quadratic.
const radixBucketMax = 32

type addrRadixScratch struct {
	cnt [1<<16 + 1]int32
	pos [1 << 16]int32
	out []types.Address
}

var addrRadixPool = sync.Pool{New: func() any { return new(addrRadixScratch) }}

func compareAddresses(a, b types.Address) int {
	x, y := binary.BigEndian.Uint64(a[:8]), binary.BigEndian.Uint64(b[:8])
	if x != y {
		if x < y {
			return -1
		}
		return 1
	}
	return bytes.Compare(a[8:], b[8:])
}

// sortAddresses sorts addrs in lexicographic byte order, the order of
// bytes.Compare. A block-sized set (the ~31k accounts a 163k-transfer block
// touches, sorted several times per block on the import's critical path:
// pending credits, the root's dirty set, CommitBlock) is distributed by its
// top 16 bits into 65,536 buckets of about one address each and every
// bucket finished in place: ~0.4 ms instead of ~9 ms for a comparison sort
// moving 20-byte elements.
func sortAddresses(addrs []types.Address) {
	n := len(addrs)
	if n < radixSortMin {
		slices.SortFunc(addrs, compareAddresses)
		return
	}
	s := addrRadixPool.Get().(*addrRadixScratch)
	defer addrRadixPool.Put(s)
	clear(s.cnt[:])
	for i := range addrs {
		s.cnt[int(binary.BigEndian.Uint16(addrs[i][:2]))+1]++
	}
	for i := 1; i < len(s.cnt); i++ {
		s.cnt[i] += s.cnt[i-1]
	}
	copy(s.pos[:], s.cnt[:1<<16])
	if cap(s.out) < n {
		s.out = make([]types.Address, n)
	}
	out := s.out[:n]
	for i := range addrs {
		b := binary.BigEndian.Uint16(addrs[i][:2])
		out[s.pos[b]] = addrs[i]
		s.pos[b]++
	}
	for b := 0; b < 1<<16; b++ {
		lo, hi := int(s.cnt[b]), int(s.cnt[b+1])
		switch {
		case hi-lo < 2:
		case hi-lo > radixBucketMax:
			slices.SortFunc(out[lo:hi], compareAddresses)
		default:
			bucket := out[lo:hi]
			for i := 1; i < len(bucket); i++ {
				for j := i; j > 0 && compareAddresses(bucket[j-1], bucket[j]) > 0; j-- {
					bucket[j-1], bucket[j] = bucket[j], bucket[j-1]
				}
			}
		}
	}
	copy(addrs, out)
}

// sortAddressesWith sorts addrs as sortAddresses does and permutes vals
// (the same length) along with them. The addresses must be distinct.
func sortAddressesWith[V any](addrs []types.Address, vals []V) {
	n := len(addrs)
	if n < radixSortMin {
		sortAddressesWithCmp(addrs, vals)
		return
	}
	s := addrRadixPool.Get().(*addrRadixScratch)
	defer addrRadixPool.Put(s)
	clear(s.cnt[:])
	for i := range addrs {
		s.cnt[int(binary.BigEndian.Uint16(addrs[i][:2]))+1]++
	}
	for i := 1; i < len(s.cnt); i++ {
		s.cnt[i] += s.cnt[i-1]
	}
	copy(s.pos[:], s.cnt[:1<<16])
	if cap(s.out) < n {
		s.out = make([]types.Address, n)
	}
	out := s.out[:n]
	ov := make([]V, n)
	for i := range addrs {
		b := binary.BigEndian.Uint16(addrs[i][:2])
		out[s.pos[b]] = addrs[i]
		ov[s.pos[b]] = vals[i]
		s.pos[b]++
	}
	for b := 0; b < 1<<16; b++ {
		lo, hi := int(s.cnt[b]), int(s.cnt[b+1])
		switch {
		case hi-lo < 2:
		case hi-lo > radixBucketMax:
			sortAddressesWithCmp(out[lo:hi], ov[lo:hi])
		default:
			bucket, bv := out[lo:hi], ov[lo:hi]
			for i := 1; i < len(bucket); i++ {
				for j := i; j > 0 && compareAddresses(bucket[j-1], bucket[j]) > 0; j-- {
					bucket[j-1], bucket[j] = bucket[j], bucket[j-1]
					bv[j-1], bv[j] = bv[j], bv[j-1]
				}
			}
		}
	}
	copy(addrs, out)
	copy(vals, ov)
}

// sortAddressesWithCmp is sortAddressesWith by a comparison sort.
func sortAddressesWithCmp[V any](addrs []types.Address, vals []V) {
	n := len(addrs)
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = int32(i)
	}
	slices.SortFunc(idx, func(a, b int32) int { return compareAddresses(addrs[a], addrs[b]) })
	oa, ov := make([]types.Address, n), make([]V, n)
	for i, k := range idx {
		oa[i], ov[i] = addrs[k], vals[k]
	}
	copy(addrs, oa)
	copy(vals, ov)
}
