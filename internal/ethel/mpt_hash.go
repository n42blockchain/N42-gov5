// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// mpt_hash.go — minimal MPT root hash for receipt/tx root verification.
// Implements Ethereum's Modified Merkle Patricia Trie root computation
// for ordered key-value lists (receipt trie, transaction trie).

package ethel

import (
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// mptNode represents a node in the Merkle Patricia Trie.
type mptNode interface {
	mptNode()
}

type mptLeaf struct {
	key   []byte // remaining nibbles
	value []byte // RLP-encoded value
}

type mptBranch struct {
	children [17]mptNode // 0-15 = nibble children, 16 = value
}

type mptExtension struct {
	key   []byte  // shared nibbles
	child mptNode
}

type mptHashNode struct {
	hash types.Hash
}

func (*mptLeaf) mptNode()      {}
func (*mptBranch) mptNode()    {}
func (*mptExtension) mptNode() {}
func (*mptHashNode) mptNode()  {}

// simpleTrie builds an MPT from ordered key-value pairs.
type simpleTrie struct {
	root mptNode
}

func newSimpleTrie() *simpleTrie {
	return &simpleTrie{}
}

func (t *simpleTrie) update(key, value []byte) {
	nibbles := keyToNibbles(key)
	t.root = insertNode(t.root, nibbles, value)
}

func (t *simpleTrie) hash() types.Hash {
	if t.root == nil {
		return types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	}
	enc := encodeNode(t.root)
	if len(enc) < 32 {
		return crypto.Keccak256Hash(enc)
	}
	return crypto.Keccak256Hash(enc)
}

func keyToNibbles(key []byte) []byte {
	nibbles := make([]byte, len(key)*2)
	for i, b := range key {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0f
	}
	return nibbles
}

func commonPrefixLen(a, b []byte) int {
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

func insertNode(node mptNode, nibbles, value []byte) mptNode {
	if node == nil {
		return &mptLeaf{key: nibbles, value: value}
	}

	switch n := node.(type) {
	case *mptLeaf:
		cp := commonPrefixLen(n.key, nibbles)
		if cp == len(n.key) && cp == len(nibbles) {
			// Same key — update value.
			return &mptLeaf{key: nibbles, value: value}
		}
		// Split into branch.
		branch := &mptBranch{}
		if cp == len(n.key) {
			branch.children[16] = &mptLeaf{key: nil, value: n.value}
			branch.children[nibbles[cp]] = insertNode(nil, nibbles[cp+1:], value)
		} else if cp == len(nibbles) {
			branch.children[16] = &mptLeaf{key: nil, value: value}
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
			n.children[16] = &mptLeaf{key: nil, value: value}
		} else {
			n.children[nibbles[0]] = insertNode(n.children[nibbles[0]], nibbles[1:], value)
		}
		return n

	case *mptExtension:
		cp := commonPrefixLen(n.key, nibbles)
		if cp == len(n.key) {
			n.child = insertNode(n.child, nibbles[cp:], value)
			return n
		}
		// Split extension.
		branch := &mptBranch{}
		if cp+1 == len(n.key) {
			branch.children[n.key[cp]] = n.child
		} else {
			branch.children[n.key[cp]] = &mptExtension{key: n.key[cp+1:], child: n.child}
		}
		branch.children[nibbles[cp]] = insertNode(nil, nibbles[cp+1:], value)
		if cp > 0 {
			return &mptExtension{key: nibbles[:cp], child: branch}
		}
		return branch
	}
	return node
}

// encodeNode returns the RLP encoding of a node. If >= 32 bytes, returns the hash.
func encodeNode(node mptNode) []byte {
	switch n := node.(type) {
	case *mptLeaf:
		hp := hexPrefixEncode(n.key, true)
		enc, _ := rlp.EncodeToBytes([]interface{}{hp, n.value})
		return enc

	case *mptBranch:
		var items [17]interface{}
		for i := 0; i < 16; i++ {
			if n.children[i] == nil {
				items[i] = []byte{}
			} else {
				child := encodeNode(n.children[i])
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
		hp := hexPrefixEncode(n.key, false)
		child := encodeNode(n.child)
		var childRef interface{}
		if len(child) >= 32 {
			childRef = crypto.Keccak256(child)
		} else {
			childRef = rlp.RawValue(child)
		}
		enc, _ := rlp.EncodeToBytes([]interface{}{hp, childRef})
		return enc

	case *mptHashNode:
		return n.hash[:]
	}
	return nil
}

// hexPrefixEncode implements the hex-prefix encoding from the Yellow Paper.
func hexPrefixEncode(nibbles []byte, leaf bool) []byte {
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
