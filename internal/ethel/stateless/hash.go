package stateless

// encodeNode returns the RLP of a node. encodeRef returns what a PARENT embeds
// for a child: the 32-byte hash (as an RLP string) if the encoding is >=32
// bytes, else the inline RLP itself. This mirrors the standard MPT rule.

func encodeNode(n node) []byte {
	switch n := n.(type) {
	case *shortNode:
		k := rlpStr(hexToCompact(n.key))
		v := encodeRef(n.val)
		return rlpList(append(k, v...))
	case *fullNode:
		var payload []byte
		for i := 0; i < 16; i++ {
			payload = append(payload, encodeRef(n.children[i])...)
		}
		// value slot
		if v, ok := n.children[16].(valueNode); ok && len(v) > 0 {
			payload = append(payload, rlpStr(v)...)
		} else {
			payload = append(payload, 0x80) // empty
		}
		return rlpList(payload)
	case valueNode:
		return rlpStr(n)
	case hashNode:
		// already a reference; callers shouldn't encode it as a node body
		return rlpStr(n)
	case nil:
		return []byte{0x80}
	default:
		panic("encodeNode: unknown node type")
	}
}

// encodeRef returns the bytes a parent embeds for child n.
func encodeRef(n node) []byte {
	switch n := n.(type) {
	case nil:
		return []byte{0x80}
	case hashNode:
		return rlpStr(n) // 32B hash → 0xa0 || hash
	case valueNode:
		return rlpStr(n)
	default:
		enc := encodeNode(n)
		if len(enc) >= 32 {
			return rlpStr(keccak(enc)) // hash reference
		}
		return enc // inline (already RLP)
	}
}

// nodeHash returns the 32-byte hash of a node's canonical RLP.
func nodeHash(n node) []byte {
	enc := encodeNode(n)
	if len(enc) < 32 {
		// An inline node has no independent 32-byte hash; the root is still
		// hashed (the root is always hashed even if <32B per Ethereum rules).
		return keccak(enc)
	}
	return keccak(enc)
}
