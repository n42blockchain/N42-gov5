package stateless

import (
	"encoding/binary"
	"fmt"
)

// Compact multiproof wire format.
//
// A standard EIP-1186 proof is a flat set of RLP nodes; the verifier hashes each
// and walks. For a multiproof that is redundant: every internal node's hash
// appears both in its parent's child slot (32 B) and is recomputable from the
// node itself. The minimal lossless representation serializes the partial trie
// AS A TREE, carrying only:
//
//	(1) structure — node kind + branch child-mask + ext/leaf nibble path
//	(2) boundary hashes — 32 B refs to UNCHANGED subtrees absent from the proof
//	    (irreducible: needed to recompute the parent)
//	(3) leaf values — account RLP / storage slot bytes
//
// ALL internal-node hashes are omitted; the verifier recomputes them bottom-up
// and checks the root against the trusted anchor. zstd barely helps (boundary
// hashes are high-entropy keccak), so this structural omission is the real win.
//
// Wire grammar (root first; recursive). All multi-byte ints big-endian; lengths
// as uvarint:
//
//	wEmpty  0x04                                  — empty trie (root==emptyRootHash)
//	wHash   0x03 hash:32                          — bare hash root / boundary ref
//	wLeaf   0x02 nibLen:uvarint nib:packed vLen:uvarint v:bytes
//	wExt    0x01 nibLen:uvarint nib:packed inProof:1 child
//	wBranch 0x00 childMask:2 inProofMask:2 [child × popcount(childMask)] valFlag:1 [vLen v]
//	          for each set bit i of childMask (ascending): inProofMask bit i set →
//	          child is an inlined node (recurse); else → 32 B boundary hash.
//	          valFlag: 1 → value slot present (uvarint len + bytes); 0 → absent.
//
// childMask/inProofMask cover children 0..15 only (value slot is the valFlag),
// so they fit uint16.

const (
	wBranch = 0x00
	wExt    = 0x01
	wLeaf   = 0x02
	wHash   = 0x03
	wEmpty  = 0x04
)

// EncodeCompactProof serializes a partial trie into the compact tree wire,
// omitting all internal-node hashes. It is FAITHFUL: a child that is a hashNode
// whose blob is present in the proof (t.nodes) is RESOLVED and inlined as a
// recursed subtree, so the whole multiproof survives the round-trip; only
// genuinely-absent children (a hashNode pointing at a subtree the proof does not
// carry) stay as 32 B boundary refs. A decoded trie therefore carries every
// proof node and can be fed to StateRootUpdater. The root is recomputed and
// checked against the trusted anchor on decode.
func EncodeCompactProof(t *partialTrie) []byte {
	if t.root == nil {
		return []byte{wEmpty}
	}
	return t.encWire(nil, t.root)
}

// childInlined reports whether child c should be inlined (recursed) rather than
// emitted as a 32 B boundary hash: true for any non-hash node, and for a
// hashNode whose blob is present in the proof map (so it can be resolved).
func (t *partialTrie) childInlined(c node) bool {
	h, ok := c.(hashNode)
	if !ok {
		return true
	}
	_, present := t.nodes[string(h)]
	return present
}

func (t *partialTrie) encWire(buf []byte, n node) []byte {
	// Resolve an in-proof hashNode so its subtree is inlined faithfully; a
	// genuinely-absent hashNode is a bare-hash boundary (valid only as a root
	// here — branch/ext children are handled as raw refs by their parent).
	if h, ok := n.(hashNode); ok {
		if blob, present := t.nodes[string(h)]; present {
			rn, err := decodeNode(h, blob)
			if err != nil {
				return append(append(buf, wHash), h...)
			}
			n = rn
		} else {
			return append(append(buf, wHash), h...)
		}
	}
	switch n := n.(type) {
	case *shortNode:
		if hasTerm(n.key) {
			buf = append(buf, wLeaf)
			buf = encNibbles(buf, n.key)
			v, _ := n.val.(valueNode)
			buf = encUvarint(buf, uint64(len(v)))
			return append(buf, v...)
		}
		buf = append(buf, wExt)
		buf = encNibbles(buf, n.key)
		if t.childInlined(n.val) {
			buf = append(buf, 1)
			return t.encWire(buf, n.val)
		}
		h := n.val.(hashNode)
		buf = append(buf, 0)
		return append(buf, h...)
	case *fullNode:
		buf = append(buf, wBranch)
		var childMask, inProofMask uint16
		for i := 0; i < 16; i++ {
			c := n.children[i]
			if c == nil {
				continue
			}
			childMask |= 1 << uint(i)
			if t.childInlined(c) {
				inProofMask |= 1 << uint(i)
			}
		}
		buf = encU16(buf, childMask)
		buf = encU16(buf, inProofMask)
		for i := 0; i < 16; i++ {
			if childMask&(1<<uint(i)) == 0 {
				continue
			}
			c := n.children[i]
			if t.childInlined(c) {
				buf = t.encWire(buf, c)
			} else {
				buf = append(buf, c.(hashNode)...)
			}
		}
		if v, ok := n.children[16].(valueNode); ok && len(v) > 0 {
			buf = append(buf, 1)
			buf = encUvarint(buf, uint64(len(v)))
			buf = append(buf, v...)
		} else {
			buf = append(buf, 0)
		}
		return buf
	default:
		panic(fmt.Sprintf("encWire: unexpected %T", n))
	}
}

// CompactProofFromNodes loads a flat RLP proof set anchored at root and returns
// its faithful compact-wire encoding (all proof nodes preserved, internal hashes
// omitted). Inverse: DecodeCompactToNodes.
func CompactProofFromNodes(root []byte, proofNodes [][]byte) ([]byte, error) {
	pt, err := newPartialTrie(root, proofNodes)
	if err != nil {
		return nil, err
	}
	return EncodeCompactProof(pt), nil
}

// DecodeCompactToNodes decodes a compact wire and re-serializes it to a flat RLP
// proof node set — the form NewStateRootUpdater/AddStorageProof consume. The
// faithful encoder guarantees this set equals the original proof's nodes (modulo
// genuinely-absent boundaries, which were never in the proof).
func DecodeCompactToNodes(wire []byte) ([][]byte, error) {
	pt, err := DecodeCompactProof(wire)
	if err != nil {
		return nil, err
	}
	return pt.toNodes(), nil
}

// toNodes re-serializes the materialized nodes of the trie (those with an
// independent ≥32 B hash) as a flat RLP node set — the inverse of
// newPartialTrie. Boundary hashNodes (genuinely-absent subtrees) emit nothing.
func (t *partialTrie) toNodes() [][]byte {
	var out [][]byte
	var walk func(n node)
	walk = func(n node) {
		switch n := n.(type) {
		case *shortNode:
			if enc := encodeNode(n); len(enc) >= 32 {
				out = append(out, enc)
			}
			walk(n.val)
		case *fullNode:
			if enc := encodeNode(n); len(enc) >= 32 {
				out = append(out, enc)
			}
			for i := 0; i < 17; i++ {
				walk(n.children[i])
			}
		}
	}
	if t.root != nil {
		walk(t.root)
	}
	return out
}

// DecodeCompactProof rebuilds a partial trie from the compact wire. The caller
// checks t.hash()==trusted anchor afterwards to prove self-consistency.
func DecodeCompactProof(wire []byte) (*partialTrie, error) {
	t := &partialTrie{nodes: map[string][]byte{}}
	if len(wire) == 0 {
		return nil, fmt.Errorf("compact: empty")
	}
	if wire[0] == wEmpty {
		return t, nil
	}
	n, rest, err := decWire(wire)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("compact: %d trailing bytes", len(rest))
	}
	t.root = n
	return t, nil
}

func decWire(b []byte) (node, []byte, error) {
	if len(b) == 0 {
		return nil, nil, fmt.Errorf("compact: truncated tag")
	}
	tag := b[0]
	b = b[1:]
	switch tag {
	case wHash:
		if len(b) < 32 {
			return nil, nil, fmt.Errorf("compact: truncated hash")
		}
		return hashNode(append([]byte(nil), b[:32]...)), b[32:], nil
	case wLeaf:
		key, b2, err := decNibbles(b)
		if err != nil {
			return nil, nil, err
		}
		v, b3, err := decBytes(b2)
		if err != nil {
			return nil, nil, err
		}
		return &shortNode{key: key, val: valueNode(v), flags: dirtyFlag()}, b3, nil
	case wExt:
		key, b2, err := decNibbles(b)
		if err != nil {
			return nil, nil, err
		}
		if len(b2) < 1 {
			return nil, nil, fmt.Errorf("compact: truncated ext flag")
		}
		inProof := b2[0]
		b2 = b2[1:]
		if inProof == 1 {
			child, b3, err := decWire(b2)
			if err != nil {
				return nil, nil, err
			}
			return &shortNode{key: key, val: child, flags: dirtyFlag()}, b3, nil
		}
		if len(b2) < 32 {
			return nil, nil, fmt.Errorf("compact: truncated ext child hash")
		}
		return &shortNode{key: key, val: hashNode(append([]byte(nil), b2[:32]...)), flags: dirtyFlag()}, b2[32:], nil
	case wBranch:
		if len(b) < 4 {
			return nil, nil, fmt.Errorf("compact: truncated branch masks")
		}
		childMask := binary.BigEndian.Uint16(b)
		inProofMask := binary.BigEndian.Uint16(b[2:])
		b = b[4:]
		fn := &fullNode{flags: dirtyFlag()}
		for i := 0; i < 16; i++ {
			if childMask&(1<<uint(i)) == 0 {
				continue
			}
			if inProofMask&(1<<uint(i)) != 0 {
				c, rest, err := decWire(b)
				if err != nil {
					return nil, nil, err
				}
				fn.children[i] = c
				b = rest
			} else {
				if len(b) < 32 {
					return nil, nil, fmt.Errorf("compact: truncated branch child hash")
				}
				fn.children[i] = hashNode(append([]byte(nil), b[:32]...))
				b = b[32:]
			}
		}
		if len(b) < 1 {
			return nil, nil, fmt.Errorf("compact: truncated branch valFlag")
		}
		hasVal := b[0]
		b = b[1:]
		if hasVal == 1 {
			v, rest, err := decBytes(b)
			if err != nil {
				return nil, nil, err
			}
			fn.children[16] = valueNode(v)
			b = rest
		}
		return fn, b, nil
	default:
		return nil, nil, fmt.Errorf("compact: unknown tag 0x%02x", tag)
	}
}

// --- encoders / decoders ---

func encU16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }

func encUvarint(b []byte, v uint64) []byte {
	var t [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(t[:], v)
	return append(b, t[:n]...)
}

func decBytes(b []byte) ([]byte, []byte, error) {
	l, n := binary.Uvarint(b)
	if n <= 0 {
		return nil, nil, fmt.Errorf("compact: bad length")
	}
	b = b[n:]
	if uint64(len(b)) < l {
		return nil, nil, fmt.Errorf("compact: truncated value")
	}
	return append([]byte(nil), b[:l]...), b[l:], nil
}

// encNibbles writes a hex-nibble slice as uvarint body-count + 1-byte terminator
// flag + packed body bytes (2 nibbles/byte, high first; trailing odd nibble in
// the high half). The leaf terminator nibble (0x10) does NOT fit in 4 bits, so
// it is carried as the flag rather than packed — body nibbles are all 0..15.
func encNibbles(b []byte, nib []byte) []byte {
	var term byte
	body := nib
	if len(nib) > 0 && nib[len(nib)-1] == 16 {
		term = 1
		body = nib[:len(nib)-1]
	}
	b = encUvarint(b, uint64(len(body)))
	b = append(b, term)
	for i := 0; i < len(body); i += 2 {
		hi := body[i] << 4
		var lo byte
		if i+1 < len(body) {
			lo = body[i+1]
		}
		b = append(b, hi|lo)
	}
	return b
}

func decNibbles(b []byte) ([]byte, []byte, error) {
	cnt, n := binary.Uvarint(b)
	if n <= 0 {
		return nil, nil, fmt.Errorf("compact: bad nibble count")
	}
	b = b[n:]
	if len(b) < 1 {
		return nil, nil, fmt.Errorf("compact: missing nibble term flag")
	}
	term := b[0]
	b = b[1:]
	packed := int((cnt + 1) / 2)
	if len(b) < packed {
		return nil, nil, fmt.Errorf("compact: truncated nibbles")
	}
	outLen := int(cnt)
	if term == 1 {
		outLen++
	}
	nib := make([]byte, outLen)
	for i := 0; i < int(cnt); i++ {
		by := b[i/2]
		if i%2 == 0 {
			nib[i] = by >> 4
		} else {
			nib[i] = by & 0x0f
		}
	}
	if term == 1 {
		nib[int(cnt)] = 16
	}
	return nib, b[packed:], nil
}
