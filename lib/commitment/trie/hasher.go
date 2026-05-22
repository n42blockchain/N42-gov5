package trie

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/rlp"
)

// encodeNode returns the canonical MPT RLP for a node.
//
// Child references are computed recursively: a child whose own
// encoded RLP is < 32 bytes is embedded directly into the parent
// list; otherwise the child is replaced by its 32-byte hash wrapped
// as an RLP byte string. This matches the standard EIP-1186 wire
// format used by both go-ethereum and erigon.
//
// Pre-condition: the witness trie produced by HexPatriciaHashed.
// GenerateWitness has fully expanded any hash references along the
// path of interest. HashNode encountered during encoding is an
// opaque reference (its 32-byte hash IS its encoding) and is
// emitted verbatim — Prove must NOT encounter unexpanded HashNodes
// on the proof path, but unrelated subtrees may still reference one.
func encodeNode(n Node) ([]byte, error) {
	switch v := n.(type) {
	case nil:
		return []byte{0x80}, nil // RLP empty string
	case *FullNode:
		return encodeFullNode(v)
	case *ShortNode:
		return encodeShortNode(v)
	case *HashNode:
		// 33-byte 0xa0||hash string. Length prefix 0xa0 = 0x80 + 0x20.
		out := make([]byte, 33)
		out[0] = 0xa0
		copy(out[1:], v.Hash[:])
		return out, nil
	case *AccountNode:
		// Encoded as RLP([nonce, balance, storageRoot, codeHash]).
		// We delegate to StateAccount.EncodeForHashing which already
		// produces that exact list, but we need to ensure Root is
		// set if Storage is present (caller's responsibility).
		buf := make([]byte, v.Account.EncodingLengthForHashing())
		v.Account.EncodeForHashing(buf)
		return buf, nil
	case ValueNode:
		return rlp.EncodeToBytes([]byte(v))
	default:
		return nil, fmt.Errorf("encodeNode: unsupported node type %T", n)
	}
}

// nodeRef returns the parent-facing reference for a child node:
//   - 33-byte (0xa0||hash) string if the encoded RLP is >= 32 bytes
//   - the inline RLP itself if < 32 bytes
//
// The two cases are distinguished by length at decode time; the
// caller (encodeFullNode / encodeShortNode) appends the result
// verbatim into the parent list payload.
func nodeRef(n Node) ([]byte, error) {
	if n == nil {
		return []byte{0x80}, nil
	}
	if h, ok := n.(*HashNode); ok {
		out := make([]byte, 33)
		out[0] = 0xa0
		copy(out[1:], h.Hash[:])
		return out, nil
	}
	enc, err := encodeNode(n)
	if err != nil {
		return nil, err
	}
	if len(enc) < 32 {
		return enc, nil
	}
	h := sha3.NewLegacyKeccak256()
	h.Write(enc)
	var sum [32]byte
	h.Sum(sum[:0])
	out := make([]byte, 33)
	out[0] = 0xa0
	copy(out[1:], sum[:])
	return out, nil
}

func encodeShortNode(n *ShortNode) ([]byte, error) {
	if n.Val == nil {
		return nil, errors.New("encodeShortNode: nil Val")
	}
	keyCompact := HexToCompact(n.Key)
	keyRLP, err := rlp.EncodeToBytes(keyCompact)
	if err != nil {
		return nil, fmt.Errorf("encode key: %w", err)
	}
	var valRLP []byte
	switch v := n.Val.(type) {
	case ValueNode:
		valRLP, err = rlp.EncodeToBytes([]byte(v))
		if err != nil {
			return nil, fmt.Errorf("encode value: %w", err)
		}
	case *AccountNode:
		// Account stored as RLP-encoded byte string (double RLP),
		// matching geth/erigon convention for account leaves.
		acRaw, err := encodeNode(v)
		if err != nil {
			return nil, err
		}
		valRLP, err = rlp.EncodeToBytes(acRaw)
		if err != nil {
			return nil, fmt.Errorf("encode account bytes: %w", err)
		}
	default:
		valRLP, err = nodeRef(n.Val)
		if err != nil {
			return nil, fmt.Errorf("encode child ref: %w", err)
		}
	}
	return rlpList(keyRLP, valRLP), nil
}

func encodeFullNode(n *FullNode) ([]byte, error) {
	parts := make([][]byte, 17)
	for i := 0; i < 16; i++ {
		ref, err := nodeRef(n.Children[i])
		if err != nil {
			return nil, fmt.Errorf("encode child %d: %w", i, err)
		}
		parts[i] = ref
	}
	// Slot 17 (value) — usually empty for branch nodes in account/
	// storage tries, but if present it's either an inline value or
	// (for account-level branches) an AccountNode.
	switch v := n.Children[16].(type) {
	case nil:
		parts[16] = []byte{0x80}
	case ValueNode:
		enc, err := rlp.EncodeToBytes([]byte(v))
		if err != nil {
			return nil, fmt.Errorf("encode value slot: %w", err)
		}
		parts[16] = enc
	case *AccountNode:
		acRaw, err := encodeNode(v)
		if err != nil {
			return nil, err
		}
		enc, err := rlp.EncodeToBytes(acRaw)
		if err != nil {
			return nil, fmt.Errorf("encode account in value slot: %w", err)
		}
		parts[16] = enc
	default:
		ref, err := nodeRef(v)
		if err != nil {
			return nil, fmt.Errorf("encode value slot ref: %w", err)
		}
		parts[16] = ref
	}
	return rlpList(parts...), nil
}

// rlpList concatenates pre-encoded items into an RLP list with the
// correct length prefix.
func rlpList(items ...[]byte) []byte {
	payloadLen := 0
	for _, it := range items {
		payloadLen += len(it)
	}
	header := encodeListHeader(payloadLen)
	out := make([]byte, 0, len(header)+payloadLen)
	out = append(out, header...)
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

func encodeListHeader(payloadLen int) []byte {
	switch {
	case payloadLen < 56:
		return []byte{byte(0xc0 + payloadLen)}
	case payloadLen < 256:
		return []byte{0xf8, byte(payloadLen)}
	case payloadLen < 65536:
		return []byte{0xf9, byte(payloadLen >> 8), byte(payloadLen)}
	default:
		return []byte{0xfa, byte(payloadLen >> 16), byte(payloadLen >> 8), byte(payloadLen)}
	}
}
