// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// DeriveShaErigon computes the canonical Ethereum MPT trie root of
// a DerivableList (transaction / receipt / withdrawal roots) using
// erigon's streaming HashBuilder + GenStructStep algorithm. Builds
// nibble-sorted (keyHex, value) entries, feeds them through a
// rlphacks.RlpSerializableBytes stream and returns EmptyRootHash
// for empty inputs. Used whenever ETH-compatible root hashing is
// required on N42 blocks and ETH EL profile output.

package hash

import (
	"bytes"
	"runtime"
	"sync"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

// DeriveShaErigon computes the canonical Ethereum MPT trie root of a
// DerivableList (transaction root, receipt root, withdrawal root) via
// erigon's HashBuilder + GenStructStep streaming algorithm.
func DeriveShaErigon(list DerivableList) types.Hash {
	n := list.Len()
	if n == 0 {
		return EmptyRootHash
	}

	// The leaf values are independent of each other and of the trie:
	// EncodeIndex(i) is a pure function of list[i], and the HashBuilder walk
	// that follows needs them one at a time in trie order. Encoding is 28% of
	// this function at a full block (0.203 of 0.734 us/tx over 22,857 leaves,
	// see BenchmarkTxRoot), and the whole function sits on the SERIAL import
	// path -- ValidateBody runs before Process, and the leader pays it again
	// when it assembles. So the encode is worth taking off the critical path,
	// and unlike splitting the trie itself it cannot change the result: the
	// bytes handed to GenStructStep are the same bytes, produced earlier.
	//
	// Sequential below the threshold, where the goroutines cost more than the
	// encode; the fleet's blocks are 22,857 leaves and always take the pool.
	values := encodeValuesParallel(list, n)

	hb := trie.NewHashBuilder(false)
	var valBuf bytes.Buffer
	// An RLP-encoded uint64 is at most 9 bytes. Expanding each byte into two
	// nibbles and appending the leaf terminator therefore needs at most 19.
	var keyRLP [9]byte
	var currBuf, succBuf [19]byte
	var groups, hasTree, hasHash []uint16
	var leafData trie.GenStructStepLeafData
	retain := func(_ []byte) bool { return false }

	// HashBuilder needs keys in nibble-lexicographic order. For RLP(index),
	// that order is deterministic: 1..127, 0, then 128 upward. Stream in that
	// order instead of allocating every key/value and sorting an entries slice.
	for pos := 0; pos < n; pos++ {
		i := deriveTrieIndex(pos, n)
		curr := deriveTrieKey(currBuf[:0], keyRLP[:0], uint64(i))
		var succ []byte
		if pos+1 < n {
			next := deriveTrieIndex(pos+1, n)
			succ = deriveTrieKey(succBuf[:0], keyRLP[:0], uint64(next))
		}

		if values != nil {
			leafData.Value = rlphacks.RlpEncodedBytes(values[i])
		} else {
			valBuf.Reset()
			list.EncodeIndex(i, &valBuf)
			leafData.Value = rlphacks.RlpEncodedBytes(valBuf.Bytes())
		}
		var err error
		groups, hasTree, hasHash, err = trie.GenStructStep(
			retain, curr, succ, hb, nil, &leafData,
			groups, hasTree, hasHash, false,
		)
		if err != nil {
			panic(err)
		}
	}

	root, err := hb.RootHash()
	if err != nil {
		panic(err)
	}
	return root
}

// deriveTrieIndex returns the pos-th integer when RLP-encoded integers in
// [0,n) are ordered lexicographically. Single-byte values 1..127 precede the
// empty-string encoding of zero (0x80); longer encodings follow numerically.
func deriveTrieIndex(pos, n int) int {
	single := n
	if single > 128 {
		single = 128
	}
	if pos < single-1 {
		return pos + 1
	}
	if pos == single-1 {
		return 0
	}
	return pos
}

func deriveTrieKey(dst, rlpBuf []byte, index uint64) []byte {
	encoded := rlp.AppendUint64(rlpBuf, index)
	for _, b := range encoded {
		dst = append(dst, b>>4, b&0x0f)
	}
	return append(dst, 0x10)
}

// parallelEncodeMinLeaves is the point below which the worker pool costs more
// than it saves. A 480M-gas block of transfers is 22,857 leaves. A var, not a
// const, so the equivalence test can force either path for the same input.
var parallelEncodeMinLeaves = 2048

// encodeValuesParallel returns the RLP-encoded leaf value for every index, or
// nil when the list is small enough that the sequential path should encode
// inline. Each worker owns a disjoint stride and its own buffer, so nothing is
// shared but the output slice, whose entries are written once each.
func encodeValuesParallel(list DerivableList, n int) [][]byte {
	if n < parallelEncodeMinLeaves {
		return nil
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16 // past this the encode is memory-bound, not CPU-bound
	}
	if workers < 2 {
		return nil
	}
	values := make([][]byte, n)
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(start int) {
			defer wg.Done()
			// One arena per worker rather than one allocation per value. The
			// values must outlive the loop, so they cannot alias a buffer the
			// next iteration overwrites; but appending into a single arena and
			// slicing it AFTER the loop is safe, because the arena stops
			// growing -- and stops moving -- once the loop ends. Copying each
			// value instead cost one allocation per transaction, which doubled
			// this function's allocation count on the import path.
			count := (n - start + workers - 1) / workers
			if count <= 0 {
				return
			}
			var buf bytes.Buffer
			// Size the arena from the first value. Leaves in one list are the
			// same kind of object, so for a block of transfers this is exact to
			// within the eighth added for slack; append still grows it correctly
			// when they are not, it just copies once more. Without the hint the
			// arena doubles its way up and allocates ~4x the bytes it keeps.
			list.EncodeIndex(start, &buf)
			first := append([]byte(nil), buf.Bytes()...)
			arena := make([]byte, 0, count*len(first)*9/8+64)
			spans := make([][2]int, 0, count)
			idxs := make([]int, 0, count)

			arena = append(arena, first...)
			spans = append(spans, [2]int{0, len(arena)})
			idxs = append(idxs, start)
			for i := start + workers; i < n; i += workers {
				buf.Reset()
				list.EncodeIndex(i, &buf)
				off := len(arena)
				arena = append(arena, buf.Bytes()...)
				spans = append(spans, [2]int{off, len(arena)})
				idxs = append(idxs, i)
			}
			// Slice only after the loop: the arena stops growing, and therefore
			// stops moving, once no more appends can happen.
			for k, i := range idxs {
				values[i] = arena[spans[k][0]:spans[k][1]:spans[k][1]]
			}
		}(w)
	}
	wg.Wait()
	return values
}
