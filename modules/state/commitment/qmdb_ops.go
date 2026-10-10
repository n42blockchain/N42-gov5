// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"slices"
	"sync"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// accountOpsParallelMin is the dirty-account count from which the account
// ops are built by several goroutines.
const accountOpsParallelMin = 4096

// buildAccountOps returns the block's account ops (key hash, encoded value
// or a delete) in map order, with capacity for extra storage ops. The key
// hash and the encoding of each account are independent of every other, so
// a block-sized set (~31k accounts at 163k transfers) is split over up to
// GOMAXPROCS workers; the ops are the same either way and are sorted after.
func buildAccountOps(accounts map[types.Address]*account.StateAccount, extra int) []qmdb.Op {
	type entry struct {
		addr types.Address
		acct *account.StateAccount
	}
	ops := make([]qmdb.Op, len(accounts), len(accounts)+extra)
	fill := func(es []entry, out []qmdb.Op) {
		for i, e := range es {
			out[i].KeyHash = qmdb.Hash(AccountKeyHash(e.addr))
			if e.acct != nil && !isAccountEmpty(e.acct) {
				out[i].Value = EncodeAccountValue(e.acct)
			}
		}
	}
	es := make([]entry, 0, len(accounts))
	for addr, acct := range accounts {
		es = append(es, entry{addr, acct})
	}
	workers := min(runtime.GOMAXPROCS(0), 8, len(es)/(accountOpsParallelMin/4))
	if len(es) < accountOpsParallelMin || workers < 2 {
		fill(es, ops)
		return ops
	}
	var wg sync.WaitGroup
	per := (len(es) + workers - 1) / workers
	for lo := 0; lo < len(es); lo += per {
		hi := min(lo+per, len(es))
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			fill(es[lo:hi], ops[lo:hi])
		}(lo, hi)
	}
	wg.Wait()
	return ops
}

// opsRadixMin is the op count from which sortOpsByKey distributes by the
// top 16 bits of the key hash first.
const opsRadixMin = 2048

// opsBucketMax is the largest bucket insertion-sorted in place; key hashes
// are uniform, so a larger one only follows a hash collision on 16 bits far
// beyond chance and takes the comparison sort.
const opsBucketMax = 32

type opsRadixScratch struct {
	cnt [1<<16 + 1]int32
	pos [1 << 16]int32
	out []qmdb.Op
}

var opsRadixPool = sync.Pool{New: func() any { return new(opsRadixScratch) }}

func compareOps(a, b qmdb.Op) int {
	x, y := binary.BigEndian.Uint64(a.KeyHash[:8]), binary.BigEndian.Uint64(b.KeyHash[:8])
	if x != y {
		if x < y {
			return -1
		}
		return 1
	}
	return bytes.Compare(a.KeyHash[8:], b.KeyHash[8:])
}

// sortOpsByKey sorts ops by key hash in bytes.Compare order, the order
// ComputeRoot applies them in (the QMDB root depends on it). A block-sized
// batch is distributed into 65,536 buckets by the top 16 bits of its
// uniformly distributed key hashes and each bucket of about one op is
// finished in place, instead of a reflection-based comparison sort of
// 56-byte elements.
func sortOpsByKey(ops []qmdb.Op) {
	n := len(ops)
	if n < opsRadixMin {
		slices.SortFunc(ops, compareOps)
		return
	}
	s := opsRadixPool.Get().(*opsRadixScratch)
	defer opsRadixPool.Put(s)
	clear(s.cnt[:])
	for i := range ops {
		s.cnt[int(binary.BigEndian.Uint16(ops[i].KeyHash[:2]))+1]++
	}
	for i := 1; i < len(s.cnt); i++ {
		s.cnt[i] += s.cnt[i-1]
	}
	copy(s.pos[:], s.cnt[:1<<16])
	if cap(s.out) < n {
		s.out = make([]qmdb.Op, n)
	}
	out := s.out[:n]
	for i := range ops {
		b := binary.BigEndian.Uint16(ops[i].KeyHash[:2])
		out[s.pos[b]] = ops[i]
		s.pos[b]++
	}
	for b := 0; b < 1<<16; b++ {
		lo, hi := int(s.cnt[b]), int(s.cnt[b+1])
		switch {
		case hi-lo < 2:
		case hi-lo > opsBucketMax:
			slices.SortFunc(out[lo:hi], compareOps)
		default:
			bucket := out[lo:hi]
			for i := 1; i < len(bucket); i++ {
				for j := i; j > 0 && compareOps(bucket[j-1], bucket[j]) > 0; j-- {
					bucket[j-1], bucket[j] = bucket[j], bucket[j-1]
				}
			}
		}
	}
	copy(ops, out)
	clear(out) // drop the scratch's references to the values
}
