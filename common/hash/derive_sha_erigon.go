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

		valBuf.Reset()
		list.EncodeIndex(i, &valBuf)
		leafData.Value = rlphacks.RlpEncodedBytes(valBuf.Bytes())
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
