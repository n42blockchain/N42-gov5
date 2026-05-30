package stateless

import (
	"github.com/n42blockchain/N42/crypto"
)

// This file defines a minimal standard-Ethereum MPT node model sufficient to
// load EIP-1186 proof nodes, mutate leaves, and recompute hashes bottom-up.
//
// Node kinds (matching the RLP wire form):
//   - fullNode  : 17 slots (16 children + a value slot, value unused for the
//                 secure account/storage tries N42 uses).
//   - shortNode : [key, val] where key is hex-nibbles; a LEAF if it ends with
//                 the 0x10 terminator, else an EXTENSION.
//   - hashNode  : a 32-byte reference to a node not materialised locally (the
//                 proof boundary). Resolved on demand from the proof node map.
//   - valueNode : a leaf's stored bytes (account RLP or storage value).
//
// Keys inside nodes are kept in HEX-NIBBLE form (one nibble per byte), with a
// trailing 0x10 terminator on leaf keys — the same convention as go-ethereum's
// trie, which keeps insert/delete reshaping simple. Compact (HP) encoding is
// applied only at RLP encode/decode time (see rlp.go).

type node interface{ cache() (hashNode, bool) }

type (
	fullNode struct {
		children [17]node // 16 branches + value slot
		flags    nodeFlag
	}
	shortNode struct {
		key   []byte // hex nibbles; leaf if key[len-1]==0x10
		val   node
		flags nodeFlag
	}
	hashNode  []byte // 32-byte reference
	valueNode []byte // leaf payload
)

// nodeFlag caches a node's computed hash so an unchanged subtree is not
// re-hashed. dirty==true means the cached hash is stale (a descendant changed).
type nodeFlag struct {
	hash  hashNode
	dirty bool
}

func (n *fullNode) cache() (hashNode, bool)  { return n.flags.hash, n.flags.dirty }
func (n *shortNode) cache() (hashNode, bool) { return n.flags.hash, n.flags.dirty }
func (n hashNode) cache() (hashNode, bool)   { return nil, true }
func (n valueNode) cache() (hashNode, bool)  { return nil, true }

func (n *fullNode) copy() *fullNode   { c := *n; return &c }
func (n *shortNode) copy() *shortNode { c := *n; return &c }

// hasTerm reports whether a hex key ends with the 0x10 leaf terminator.
func hasTerm(k []byte) bool { return len(k) > 0 && k[len(k)-1] == 16 }

// keybytesToHex expands key bytes to nibbles + 0x10 terminator.
func keybytesToHex(b []byte) []byte {
	nib := make([]byte, len(b)*2+1)
	for i, x := range b {
		nib[2*i] = x >> 4
		nib[2*i+1] = x & 0x0f
	}
	nib[len(nib)-1] = 16
	return nib
}

// prefixLen returns the length of the common prefix of a and b.
func prefixLen(a, b []byte) int {
	i, n := 0, len(a)
	if len(b) < n {
		n = len(b)
	}
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func keccak(b []byte) []byte { return crypto.Keccak256(b) }
