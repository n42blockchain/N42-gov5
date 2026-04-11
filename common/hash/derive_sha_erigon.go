// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package hash

import (
	"bytes"
	"sort"

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

	type kv struct {
		keyHex []byte
		val    []byte
	}
	entries := make([]kv, n)
	var valBuf bytes.Buffer
	var keyBuf [10]byte
	for i := 0; i < n; i++ {
		k := rlp.AppendUint64(keyBuf[:0], uint64(i))
		hex := make([]byte, 0, len(k)*2+1)
		for _, b := range k {
			hex = append(hex, b>>4, b&0x0f)
		}
		hex = append(hex, 0x10) // leaf terminator
		entries[i].keyHex = hex
		valBuf.Reset()
		list.EncodeIndex(i, &valBuf)
		entries[i].val = append([]byte(nil), valBuf.Bytes()...)
	}
	// HashBuilder requires keys in nibble-sorted order.
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].keyHex, entries[j].keyHex) < 0
	})

	hb := trie.NewHashBuilder(false)
	var curr, succ, currVal []byte
	var groups, hasTree, hasHash []uint16
	var leafData trie.GenStructStepLeafData
	retain := func(_ []byte) bool { return false }

	for _, e := range entries {
		succ = e.keyHex
		if len(curr) > 0 {
			leafData.Value = rlphacks.RlpEncodedBytes(currVal)
			var err error
			groups, hasTree, hasHash, err = trie.GenStructStep(
				retain, curr, succ, hb, nil, &leafData,
				groups, hasTree, hasHash, false,
			)
			if err != nil {
				panic(err)
			}
		}
		curr = succ
		currVal = e.val
	}
	leafData.Value = rlphacks.RlpEncodedBytes(currVal)
	if _, _, _, err := trie.GenStructStep(
		retain, curr, []byte{}, hb, nil, &leafData,
		groups, hasTree, hasHash, false,
	); err != nil {
		panic(err)
	}

	root, err := hb.RootHash()
	if err != nil {
		panic(err)
	}
	return root
}
