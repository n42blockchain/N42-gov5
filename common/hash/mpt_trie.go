// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// mpt_trie.go — Ethereum-compatible MPT root hash for DerivableList.
// Implements the Modified Merkle Patricia Trie root computation matching
// go-ethereum's DeriveSha with StackTrie. Used for receipt/tx/withdrawal
// roots when ETH-compatibility is required.

package hash

import (
	"bytes"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// DeriveShaETH computes the Ethereum-standard MPT trie root of a
// DerivableList. This is the canonical method used by geth for
// receipt roots, transaction roots, and withdrawal roots.
//
// N42 native blocks use DeriveSha (keccak-concat) for simplicity.
// ETH EL mode and cross-chain verification use DeriveShaETH.
func DeriveShaETH(list DerivableList) types.Hash {
	if list == nil || list.Len() == 0 {
		return EmptyRootHash
	}

	var keybuf, valbuf bytes.Buffer
	t := NewMPTTrie()
	for i := 0; i < list.Len(); i++ {
		keybuf.Reset()
		rlp.Encode(&keybuf, uint(i))
		valbuf.Reset()
		list.EncodeIndex(i, &valbuf)
		t.Update(keybuf.Bytes(), valbuf.Bytes())
	}
	return t.Hash()
}

// --- MPT trie implementation ---

type mptNode interface{ mptNode() }

type mptLeaf struct {
	key   []byte
	value []byte
}
type mptBranch struct{ children [17]mptNode }
type mptExtension struct {
	key   []byte
	child mptNode
}

func (*mptLeaf) mptNode()      {}
func (*mptBranch) mptNode()    {}
func (*mptExtension) mptNode() {}

// MPTTrie builds a Merkle Patricia Trie from key-value pairs and
// computes the root hash. Exported for use in state root verification.
type MPTTrie struct{ root mptNode }

func NewMPTTrie() *MPTTrie { return &MPTTrie{} }

func (t *MPTTrie) Update(key, value []byte) {
	t.root = mptInsert(t.root, keyToNibbles(key), value)
}

func (t *MPTTrie) Hash() types.Hash {
	if t.root == nil {
		return EmptyRootHash
	}
	return crypto.Keccak256Hash(mptEncode(t.root))
}

func keyToNibbles(key []byte) []byte {
	nibbles := make([]byte, len(key)*2)
	for i, b := range key {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0f
	}
	return nibbles
}

func mptInsert(node mptNode, nibbles, value []byte) mptNode {
	if node == nil {
		return &mptLeaf{key: nibbles, value: value}
	}
	switch n := node.(type) {
	case *mptLeaf:
		cp := commonPrefix(n.key, nibbles)
		if cp == len(n.key) && cp == len(nibbles) {
			return &mptLeaf{key: nibbles, value: value}
		}
		branch := &mptBranch{}
		if cp == len(n.key) {
			branch.children[16] = &mptLeaf{value: n.value}
			branch.children[nibbles[cp]] = mptInsert(nil, nibbles[cp+1:], value)
		} else if cp == len(nibbles) {
			branch.children[16] = &mptLeaf{value: value}
			branch.children[n.key[cp]] = &mptLeaf{key: n.key[cp+1:], value: n.value}
		} else {
			branch.children[n.key[cp]] = &mptLeaf{key: n.key[cp+1:], value: n.value}
			branch.children[nibbles[cp]] = &mptLeaf{key: nibbles[cp+1:], value: value}
		}
		if cp > 0 {
			return &mptExtension{key: nibbles[:cp], child: branch}
		}
		return branch
	case *mptBranch:
		if len(nibbles) == 0 {
			n.children[16] = &mptLeaf{value: value}
		} else {
			n.children[nibbles[0]] = mptInsert(n.children[nibbles[0]], nibbles[1:], value)
		}
		return n
	case *mptExtension:
		cp := commonPrefix(n.key, nibbles)
		if cp == len(n.key) {
			n.child = mptInsert(n.child, nibbles[cp:], value)
			return n
		}
		branch := &mptBranch{}
		if cp+1 == len(n.key) {
			branch.children[n.key[cp]] = n.child
		} else {
			branch.children[n.key[cp]] = &mptExtension{key: n.key[cp+1:], child: n.child}
		}
		branch.children[nibbles[cp]] = mptInsert(nil, nibbles[cp+1:], value)
		if cp > 0 {
			return &mptExtension{key: nibbles[:cp], child: branch}
		}
		return branch
	}
	return node
}

func commonPrefix(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func mptEncode(node mptNode) []byte {
	switch n := node.(type) {
	case *mptLeaf:
		hp := hexPrefix(n.key, true)
		enc, _ := rlp.EncodeToBytes([]interface{}{hp, n.value})
		return enc
	case *mptBranch:
		var items [17]interface{}
		for i := 0; i < 16; i++ {
			if n.children[i] == nil {
				items[i] = []byte{}
			} else {
				child := mptEncode(n.children[i])
				if len(child) >= 32 {
					items[i] = crypto.Keccak256(child)
				} else {
					items[i] = rlp.RawValue(child)
				}
			}
		}
		if n.children[16] != nil {
			items[16] = n.children[16].(*mptLeaf).value
		} else {
			items[16] = []byte{}
		}
		enc, _ := rlp.EncodeToBytes(items)
		return enc
	case *mptExtension:
		hp := hexPrefix(n.key, false)
		child := mptEncode(n.child)
		var childRef interface{}
		if len(child) >= 32 {
			childRef = crypto.Keccak256(child)
		} else {
			childRef = rlp.RawValue(child)
		}
		enc, _ := rlp.EncodeToBytes([]interface{}{hp, childRef})
		return enc
	}
	return nil
}

// hexPrefix implements the hex-prefix encoding (Yellow Paper Appendix C).
func hexPrefix(nibbles []byte, leaf bool) []byte {
	var prefix byte
	if leaf {
		prefix = 2
	}
	if len(nibbles)%2 == 1 {
		prefix |= 1
		buf := make([]byte, len(nibbles)/2+1)
		buf[0] = prefix<<4 | nibbles[0]
		for i := 1; i < len(nibbles); i += 2 {
			buf[i/2+1] = nibbles[i]<<4 | nibbles[i+1]
		}
		return buf
	}
	buf := make([]byte, len(nibbles)/2+1)
	buf[0] = prefix << 4
	for i := 0; i < len(nibbles); i += 2 {
		buf[i/2+1] = nibbles[i]<<4 | nibbles[i+1]
	}
	return buf
}
