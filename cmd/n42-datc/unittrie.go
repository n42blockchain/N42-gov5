// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// unittrie.go — an in-memory, incrementally hashed MPT over one fold unit.
//
// A fold unit is a subtree the reader folds from the leaf history (the keys
// below one nibble prefix). derive-ns replays a unit's history block by block
// and needs the unit's root hash after every block that touched it; rebuilding
// the subtree from its leaves each time (what the reader's fold does once per
// proof) would cost the unit's size per change. Here an update rehashes only
// the nodes on the changed key's path.
//
// The node RLP is the one mptNodeRLP (proof.go) produces, so the hash is the
// reader's fold hash by construction; unittrie_test.go checks that against
// both mptNodeRLP and the GenStructStep fold on random histories.
package main

import (
	"bytes"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/types"
)

const (
	utLeaf = iota
	utExt
	utBranch
)

type utNode struct {
	kind  uint8
	path  []byte       // leaf / extension nibbles
	item  []byte       // leaf: the value's complete RLP item
	child *utNode      // extension
	kids  *[16]*utNode // branch
	// ref caches what the parent embeds: the node's RLP when shorter than 32
	// bytes, else the RLP string of its hash (held in refBuf). nil = dirty.
	ref    []byte
	refBuf [33]byte
}

// unitTrie is the MPT of one unit. Keys are nibble strings of one fixed
// length (the key nibbles below the unit's prefix), so no key is a prefix of
// another and branches carry no value.
type unitTrie struct {
	root *utNode
	n    int
	// hash caches rootHash while the root is clean.
	hash   types.Hash
	hashOK bool
	h      utHasher
}

// update sets key to item (the value's complete RLP item); a nil item deletes
// the key. It reports whether the trie changed.
func (t *unitTrie) update(key, item []byte) bool {
	var changed bool
	if item == nil {
		t.root, changed = utDelete(t.root, key)
		if changed {
			t.n--
			t.hashOK = false
		}
		return changed
	}
	var added bool
	t.root, changed, added = utInsert(t.root, key, item)
	if added {
		t.n++
	}
	if changed {
		t.hashOK = false
	}
	return changed
}

// rootHash is keccak(RLP(root)), the hash a parent branch stores for this
// unit; ok=false for an empty unit.
func (t *unitTrie) rootHash() (h types.Hash, ok bool) {
	if t.root == nil {
		return h, false
	}
	if !t.hashOK {
		// The root is always referenced by hash, even when its RLP is short
		// enough to be embedded anywhere else.
		enc := t.h.encode(t.root)
		t.h.sum(enc, t.hash[:])
		t.hashOK = true
	}
	return t.hash, true
}

func utCommon(a, b []byte) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func utCopy(b []byte) []byte { return append([]byte(nil), b...) }

func utConcat(a []byte, b ...[]byte) []byte {
	out := append([]byte(nil), a...)
	for _, x := range b {
		out = append(out, x...)
	}
	return out
}

func utDirty(n *utNode) { n.ref = nil }

// utWrap puts an extension over n for a non-empty shared prefix.
func utWrap(prefix []byte, n *utNode) *utNode {
	if len(prefix) == 0 {
		return n
	}
	return &utNode{kind: utExt, path: utCopy(prefix), child: n}
}

// utSplit builds the branch where an existing node reached through `path` and
// a new leaf for `key` diverge after cp shared nibbles. old(rest) returns the
// existing side re-rooted below the branch for its remaining path.
func utSplit(path, key, item []byte, cp int, old func(rest []byte) *utNode) *utNode {
	br := &utNode{kind: utBranch, kids: new([16]*utNode)}
	br.kids[path[cp]] = old(path[cp+1:])
	br.kids[key[cp]] = &utNode{kind: utLeaf, path: utCopy(key[cp+1:]), item: item}
	return utWrap(key[:cp], br)
}

func utInsert(n *utNode, key, item []byte) (out *utNode, changed, added bool) {
	if n == nil {
		return &utNode{kind: utLeaf, path: utCopy(key), item: item}, true, true
	}
	switch n.kind {
	case utLeaf:
		cp := utCommon(n.path, key)
		if cp == len(n.path) && cp == len(key) {
			if bytes.Equal(n.item, item) {
				return n, false, false
			}
			n.item = item
			utDirty(n)
			return n, true, false
		}
		leafItem := n.item
		return utSplit(n.path, key, item, cp, func(rest []byte) *utNode {
			return &utNode{kind: utLeaf, path: utCopy(rest), item: leafItem}
		}), true, true
	case utExt:
		cp := utCommon(n.path, key)
		if cp == len(n.path) {
			c, ch, ad := utInsert(n.child, key[cp:], item)
			if ch {
				n.child = c
				utDirty(n)
			}
			return n, ch, ad
		}
		child := n.child
		return utSplit(n.path, key, item, cp, func(rest []byte) *utNode {
			return utWrap(rest, child)
		}), true, true
	default:
		c, ch, ad := utInsert(n.kids[key[0]], key[1:], item)
		if ch {
			n.kids[key[0]] = c
			utDirty(n)
		}
		return n, ch, ad
	}
}

// utPrepend re-roots n one level up: the nibbles in `prefix` now lead to it.
func utPrepend(prefix []byte, n *utNode) *utNode {
	switch n.kind {
	case utLeaf:
		return &utNode{kind: utLeaf, path: utConcat(prefix, n.path), item: n.item}
	case utExt:
		return &utNode{kind: utExt, path: utConcat(prefix, n.path), child: n.child}
	default:
		return utWrap(prefix, n)
	}
}

func utDelete(n *utNode, key []byte) (*utNode, bool) {
	if n == nil {
		return nil, false
	}
	switch n.kind {
	case utLeaf:
		if bytes.Equal(n.path, key) {
			return nil, true
		}
		return n, false
	case utExt:
		if len(key) < len(n.path) || !bytes.Equal(n.path, key[:len(n.path)]) {
			return n, false
		}
		c, ch := utDelete(n.child, key[len(n.path):])
		if !ch {
			return n, false
		}
		if c == nil {
			return nil, true
		}
		if c.kind == utBranch {
			n.child = c
			utDirty(n)
			return n, true
		}
		return utPrepend(n.path, c), true // the branch below collapsed
	default:
		c, ch := utDelete(n.kids[key[0]], key[1:])
		if !ch {
			return n, false
		}
		n.kids[key[0]] = c
		utDirty(n)
		last, count := -1, 0
		for i, k := range n.kids {
			if k != nil {
				last = i
				count++
			}
		}
		switch count {
		case 0:
			return nil, true
		case 1:
			return utPrepend([]byte{byte(last)}, n.kids[last]), true
		}
		return n, true
	}
}

// utHasher is the scratch state of one trie's hashing: derive-ns rehashes a
// few nodes per leaf-history row, billions of times, so node encoding appends
// into one buffer and the keccak state is reused instead of allocated.
type utHasher struct {
	k   keccakState
	buf []byte
}

type keccakState interface {
	Reset()
	Write([]byte) (int, error)
	Read([]byte) (int, error)
}

func (h *utHasher) sum(enc []byte, out []byte) {
	if h.k == nil {
		h.k = sha3.NewLegacyKeccak256().(keccakState)
	}
	h.k.Reset()
	h.k.Write(enc)
	h.k.Read(out)
}

// rlpHeader appends the RLP prefix of a string (base 0x80) or list (0xc0)
// payload of the given length.
func rlpHeader(dst []byte, base byte, n int) []byte {
	if n < 56 {
		return append(dst, base+byte(n))
	}
	var lenb [8]byte
	i := len(lenb)
	for v := n; v > 0; v >>= 8 {
		i--
		lenb[i] = byte(v)
	}
	dst = append(dst, base+55+byte(len(lenb)-i))
	return append(dst, lenb[i:]...)
}

// appendHexPrefixStr appends the RLP string of the hex-prefix encoding of
// nibbles (yellow paper HP, leaf flag = 2).
func appendHexPrefixStr(dst []byte, nibbles []byte, leaf bool) []byte {
	flag := byte(0)
	if leaf {
		flag = 2
	}
	n := 1 + len(nibbles)/2
	first := flag << 4
	if len(nibbles)%2 == 1 {
		first = (flag+1)<<4 | nibbles[0]
		nibbles = nibbles[1:]
	}
	if n == 1 && first < 0x80 {
		return append(dst, first)
	}
	dst = rlpHeader(dst, 0x80, n)
	dst = append(dst, first)
	for i := 0; i+1 < len(nibbles); i += 2 {
		dst = append(dst, nibbles[i]<<4|nibbles[i+1])
	}
	return dst
}

// encode appends n's RLP to h.buf[:0] and returns it (valid until the next
// encode). Child refs are resolved first: they use the same buffer.
func (h *utHasher) encode(n *utNode) []byte {
	switch n.kind {
	case utLeaf:
		hp := appendHexPrefixStr(h.buf[:0], n.path, true)
		payload := len(hp) + len(n.item)
		// Build header + payload; hp sits at the buffer start, so assemble
		// into the tail and return that part.
		start := len(hp)
		out := rlpHeader(hp, 0xc0, payload)
		out = append(out, hp[:start]...)
		out = append(out, n.item...)
		h.buf = out[:0]
		return out[start:]
	case utExt:
		ref := h.ref(n.child)
		hp := appendHexPrefixStr(h.buf[:0], n.path, false)
		start := len(hp)
		out := rlpHeader(hp, 0xc0, start+len(ref))
		out = append(out, hp[:start]...)
		out = append(out, ref...)
		h.buf = out[:0]
		return out[start:]
	default:
		payload := 1 // the empty value slot
		for _, k := range n.kids {
			if k != nil {
				payload += len(h.ref(k))
			} else {
				payload++
			}
		}
		out := rlpHeader(h.buf[:0], 0xc0, payload)
		for _, k := range n.kids {
			if k != nil {
				out = append(out, k.ref...)
			} else {
				out = append(out, 0x80)
			}
		}
		out = append(out, 0x80)
		h.buf = out[:0]
		return out
	}
}

// ref is what n's parent embeds: n's RLP when shorter than 32 bytes, else the
// RLP string of its hash. Cached on the node until it is dirtied.
func (h *utHasher) ref(n *utNode) []byte {
	if n.ref != nil {
		return n.ref
	}
	enc := h.encode(n)
	if len(enc) < 32 {
		n.ref = append([]byte(nil), enc...) // embedded verbatim
		return n.ref
	}
	n.refBuf[0] = 0xa0
	h.sum(enc, n.refBuf[1:])
	n.ref = n.refBuf[:]
	return n.ref
}
