package stateless

import (
	"errors"
	"fmt"
)

// decodeNode parses an EIP-1186 / standard MPT node from its RLP bytes into the
// in-memory model. hash is keccak(buf) (the reference the parent holds). It is
// stored in the node flag so an untouched node re-emits its original hash.
func decodeNode(hash, buf []byte) (node, error) {
	if len(buf) == 0 {
		return nil, errors.New("decode: empty node")
	}
	items, isList, err := splitListItems(buf)
	if err != nil {
		return nil, err
	}
	switch len(items) {
	case 2:
		return decodeShort(hash, items, isList)
	case 17:
		return decodeFull(hash, items, isList)
	default:
		return nil, fmt.Errorf("decode: %d-item node (want 2 or 17)", len(items))
	}
}

func decodeShort(hash []byte, items [][]byte, isList []bool) (node, error) {
	key := compactToHex(items[0]) // items[0] is always a string (HP key)
	flag := nodeFlag{hash: hashNode(hash)}
	if hasTerm(key) {
		// leaf — value is items[1] (a byte string)
		if isList[1] {
			return nil, errors.New("decode: leaf value is a list")
		}
		return &shortNode{key: key, val: valueNode(append([]byte(nil), items[1]...)), flags: flag}, nil
	}
	// extension — items[1] is a child ref (32B hash string) or an inline node (list)
	child, err := decodeRef(items[1], isList[1])
	if err != nil {
		return nil, err
	}
	return &shortNode{key: key, val: child, flags: flag}, nil
}

func decodeFull(hash []byte, items [][]byte, isList []bool) (node, error) {
	n := &fullNode{flags: nodeFlag{hash: hashNode(hash)}}
	for i := 0; i < 16; i++ {
		if len(items[i]) == 0 && !isList[i] {
			continue // empty slot
		}
		c, err := decodeRef(items[i], isList[i])
		if err != nil {
			return nil, fmt.Errorf("child %d: %w", i, err)
		}
		n.children[i] = c
	}
	// value slot (items[16]); secure tries leave it empty
	if len(items[16]) > 0 && !isList[16] {
		n.children[16] = valueNode(append([]byte(nil), items[16]...))
	}
	return n, nil
}

// decodeRef turns a child reference into a node. A 32-byte string ref becomes a
// hashNode (resolved later from the proof map). An inline list (<32B encoded)
// is decoded in place; it has no independent hash.
func decodeRef(item []byte, isList bool) (node, error) {
	if isList {
		// inline node: re-wrap the raw list bytes and decode (no own hash)
		return decodeNode(nil, item)
	}
	switch len(item) {
	case 0:
		return nil, nil
	case 32:
		return hashNode(append([]byte(nil), item...)), nil
	default:
		return nil, fmt.Errorf("decode: bad child ref len %d", len(item))
	}
}
