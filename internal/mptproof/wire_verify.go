package mptproof

import (
	"bytes"
	"errors"
	"fmt"
)

// VerifyStandardProof is an EIP-1186 / standard-MPT proof verifier
// independent of how the proof was assembled. Returns (value, true,
// nil) if the proof is valid and the key is included, (nil, false,
// nil) if the proof is valid and proves NON-inclusion, or (_, _, err)
// if the proof bytes are malformed / inconsistent.
//
// Algorithm:
//
//	expected := root
//	for each node in proof:
//	    if keccak(node) != expected: return error
//	    decode node, walk one step using the next nibble of hashedKey
//	    if reached a leaf node whose remaining key matches: return value
//	    else: expected = next child hash (or extension's hash)
//	if proof exhausted without landing: return error
//
// Used as oracle in tests — if ProofBytes() output passes this
// verifier against the stored stateRoot, we're emitting valid bytes.
func VerifyStandardProof(proofBytes [][]byte, root [32]byte, hashedKey []byte) (value []byte, found bool, err error) {
	if len(proofBytes) == 0 {
		return nil, false, errors.New("verify: empty proof")
	}
	keyNibbles := nibblesOf(hashedKey)

	expected := root
	pos := 0

	for i, node := range proofBytes {
		got := keccak256(node)
		if got != expected {
			return nil, false, fmt.Errorf("verify: node %d hash mismatch: got 0x%x want 0x%x",
				i, got[:8], expected[:8])
		}
		// Decode the node as an RLP list of items.
		items, err := decodeList(node)
		if err != nil {
			return nil, false, fmt.Errorf("verify: node %d decode: %w", i, err)
		}
		switch len(items) {
		case 17:
			// Branch node — descend into child at nibble keyNibbles[pos].
			if pos >= len(keyNibbles) {
				// Walked off end — value is items[16].
				return items[16], len(items[16]) > 0, nil
			}
			n := keyNibbles[pos]
			child := items[n]
			pos++
			if len(child) == 0 {
				return nil, false, nil // non-inclusion proof valid
			}
			if len(child) == 32 {
				copy(expected[:], child)
				continue
			}
			// Inline child (length < 32) — treat as embedded RLP node.
			// We don't follow inline children in this oracle to keep
			// it simple; this means we cannot fully verify proofs
			// that end at an inline leaf. Phase D.x covers this.
			return nil, false, fmt.Errorf("verify: inline child at node %d (oracle limit)", i)
		case 2:
			// Extension or leaf node — first item is HP-encoded path.
			hp := items[0]
			if len(hp) == 0 {
				return nil, false, fmt.Errorf("verify: node %d has empty HP key", i)
			}
			nib, isLeaf := decodeHP(hp)
			// Check the HP nibbles match our remaining keyNibbles.
			if pos+len(nib) > len(keyNibbles) {
				return nil, false, fmt.Errorf("verify: HP-key length %d would exceed remaining %d",
					len(nib), len(keyNibbles)-pos)
			}
			if !bytesEq(nib, keyNibbles[pos:pos+len(nib)]) {
				// Path diverges — proves NON-inclusion at this point.
				return nil, false, nil
			}
			pos += len(nib)
			if isLeaf {
				// items[1] is the value.
				if pos != len(keyNibbles) {
					return nil, false, fmt.Errorf("verify: leaf reached but key has %d nibbles remaining",
						len(keyNibbles)-pos)
				}
				return items[1], true, nil
			}
			// Extension: items[1] is child hash or inline.
			child := items[1]
			if len(child) == 32 {
				copy(expected[:], child)
				continue
			}
			return nil, false, fmt.Errorf("verify: inline child in extension at node %d (oracle limit)", i)
		default:
			return nil, false, fmt.Errorf("verify: node %d has %d items (want 2 or 17)", i, len(items))
		}
	}
	return nil, false, errors.New("verify: proof exhausted without conclusion")
}

// decodeList parses an RLP list and returns the raw bytes of each
// item. Items themselves may be byte strings (with the length prefix
// stripped) OR sub-lists left RLP-encoded. We use this only for
// proof nodes which are always 2-item or 17-item lists; the items
// inside are simple byte strings (HP-key, value, child-hash, or empty).
func decodeList(b []byte) ([][]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("rlp: empty input")
	}
	first := b[0]
	var payload []byte
	switch {
	case first >= 0xc0 && first <= 0xf7:
		// Short list: payload follows directly.
		payloadLen := int(first - 0xc0)
		if len(b) < 1+payloadLen {
			return nil, fmt.Errorf("rlp: short list truncated (%d < %d)", len(b)-1, payloadLen)
		}
		payload = b[1 : 1+payloadLen]
	case first >= 0xf8:
		lenOfLen := int(first - 0xf7)
		if len(b) < 1+lenOfLen {
			return nil, errors.New("rlp: long list len truncated")
		}
		var payloadLen int
		for j := 0; j < lenOfLen; j++ {
			payloadLen = payloadLen<<8 | int(b[1+j])
		}
		if len(b) < 1+lenOfLen+payloadLen {
			return nil, fmt.Errorf("rlp: long list payload truncated (%d < %d)",
				len(b)-1-lenOfLen, payloadLen)
		}
		payload = b[1+lenOfLen : 1+lenOfLen+payloadLen]
	default:
		return nil, fmt.Errorf("rlp: not a list (first byte 0x%02x)", first)
	}

	var items [][]byte
	for len(payload) > 0 {
		item, rest, err := decodeOneItem(payload)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		payload = rest
	}
	return items, nil
}

// decodeOneItem returns one decoded byte-string item (with its RLP
// header stripped) and the rest of the buffer. For lists encountered
// at this level we return them RLP-encoded so the caller can
// recursively decode if needed; for proof nodes we don't expect
// nested lists in the items, so this should never trigger.
func decodeOneItem(b []byte) (item, rest []byte, err error) {
	if len(b) == 0 {
		return nil, nil, errors.New("rlp: empty item")
	}
	first := b[0]
	switch {
	case first < 0x80:
		// Single-byte item: value is first itself.
		return []byte{first}, b[1:], nil
	case first <= 0xb7:
		l := int(first - 0x80)
		if len(b) < 1+l {
			return nil, nil, fmt.Errorf("rlp: short string truncated (%d < %d)", len(b)-1, l)
		}
		return b[1 : 1+l], b[1+l:], nil
	case first <= 0xbf:
		lenOfLen := int(first - 0xb7)
		if len(b) < 1+lenOfLen {
			return nil, nil, errors.New("rlp: long-string len truncated")
		}
		var l int
		for j := 0; j < lenOfLen; j++ {
			l = l<<8 | int(b[1+j])
		}
		if len(b) < 1+lenOfLen+l {
			return nil, nil, fmt.Errorf("rlp: long-string payload truncated (%d < %d)",
				len(b)-1-lenOfLen, l)
		}
		return b[1+lenOfLen : 1+lenOfLen+l], b[1+lenOfLen+l:], nil
	case first <= 0xf7:
		// Short list — return whole sub-list bytes (caller may decode further).
		l := int(first - 0xc0)
		end := 1 + l
		if len(b) < end {
			return nil, nil, errors.New("rlp: short sub-list truncated")
		}
		return b[:end], b[end:], nil
	default:
		lenOfLen := int(first - 0xf7)
		if len(b) < 1+lenOfLen {
			return nil, nil, errors.New("rlp: long sub-list len truncated")
		}
		var l int
		for j := 0; j < lenOfLen; j++ {
			l = l<<8 | int(b[1+j])
		}
		end := 1 + lenOfLen + l
		if len(b) < end {
			return nil, nil, errors.New("rlp: long sub-list truncated")
		}
		return b[:end], b[end:], nil
	}
}

// decodeHP reverses hexToCompact. Returns (nibbles, isLeaf).
//
//	first byte: 0b TTLN xxxx
//	  T = 0x20 → leaf, 0x00 → extension
//	  L = 0x10 → odd-length (first nibble is in low 4 bits of first byte)
//	             0x00 → even-length (low 4 bits are padding 0)
//	rest = packed nibbles (two per byte, high then low)
func decodeHP(hp []byte) (nibbles []byte, isLeaf bool) {
	if len(hp) == 0 {
		return nil, false
	}
	first := hp[0]
	isLeaf = first&0x20 != 0
	isOdd := first&0x10 != 0
	var out []byte
	if isOdd {
		out = append(out, first&0x0f)
	}
	for _, b := range hp[1:] {
		out = append(out, b>>4, b&0x0f)
	}
	return out, isLeaf
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ = bytes.Equal // keep bytes import for future use
