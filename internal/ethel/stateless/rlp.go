package stateless

import (
	"errors"
	"fmt"
)

// Minimal RLP for MPT nodes. Self-contained so the package has no dependency on
// mptproof's private helpers. Only the shapes that appear in trie nodes are
// handled: byte strings and lists of byte strings / nested short lists.

// --- encode ---

func encodeLen(l int, offsetShort, offsetLong byte) []byte {
	if l < 56 {
		return []byte{offsetShort + byte(l)}
	}
	// big-endian length, minimal bytes
	var lb []byte
	for x := l; x > 0; x >>= 8 {
		lb = append([]byte{byte(x)}, lb...)
	}
	return append([]byte{offsetLong + byte(len(lb))}, lb...)
}

// rlpStr encodes a byte string.
func rlpStr(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return b
	}
	return append(encodeLen(len(b), 0x80, 0xb7), b...)
}

// rlpList wraps already-encoded items as a list.
func rlpList(payload []byte) []byte {
	return append(encodeLen(len(payload), 0xc0, 0xf7), payload...)
}

// --- compact (HP) encoding for node keys ---

// hexToCompact converts hex nibbles (optionally 0x10-terminated) to HP bytes.
func hexToCompact(hex []byte) []byte {
	term := byte(0)
	if hasTerm(hex) {
		term = 1
		hex = hex[:len(hex)-1]
	}
	buf := make([]byte, len(hex)/2+1)
	buf[0] = term << 5 // bit 5 = terminator(leaf)
	if len(hex)&1 == 1 {
		buf[0] |= 1 << 4 // odd flag
		buf[0] |= hex[0] // first nibble
		hex = hex[1:]
	}
	for i := 0; i < len(hex); i += 2 {
		buf[i/2+1] = hex[i]<<4 | hex[i+1]
	}
	return buf
}

// compactToHex reverses hexToCompact.
func compactToHex(compact []byte) []byte {
	if len(compact) == 0 {
		return []byte{16}
	}
	base := keybytesToHexNoTerm(compact)
	// delete terminator flag
	if base[0] < 2 {
		base = base[:len(base)-1]
	}
	// apply odd flag
	chop := 2 - base[0]&1
	return base[chop:]
}

func keybytesToHexNoTerm(b []byte) []byte {
	nib := make([]byte, len(b)*2)
	for i, x := range b {
		nib[2*i] = x >> 4
		nib[2*i+1] = x & 0x0f
	}
	return nib
}

// --- decode (list of items, items are byte strings or nested RLP) ---

// rlpSplitList returns the payload of a list and the rest after it.
func rlpSplitList(b []byte) (payload, rest []byte, err error) {
	if len(b) == 0 {
		return nil, nil, errors.New("rlp: empty")
	}
	first := b[0]
	switch {
	case first >= 0xc0 && first <= 0xf7:
		l := int(first - 0xc0)
		if len(b) < 1+l {
			return nil, nil, errors.New("rlp: short list truncated")
		}
		return b[1 : 1+l], b[1+l:], nil
	case first >= 0xf8:
		ll := int(first - 0xf7)
		if len(b) < 1+ll {
			return nil, nil, errors.New("rlp: long list len truncated")
		}
		l := 0
		for i := 0; i < ll; i++ {
			l = l<<8 | int(b[1+i])
		}
		if len(b) < 1+ll+l {
			return nil, nil, errors.New("rlp: long list payload truncated")
		}
		return b[1+ll : 1+ll+l], b[1+ll+l:], nil
	default:
		return nil, nil, fmt.Errorf("rlp: not a list (0x%02x)", first)
	}
}

// rlpSplitItem returns one item (byte string content, OR raw bytes of a nested
// list) and the rest. isList tells which.
func rlpSplitItem(b []byte) (content []byte, rest []byte, isList bool, err error) {
	if len(b) == 0 {
		return nil, nil, false, errors.New("rlp: empty item")
	}
	first := b[0]
	switch {
	case first < 0x80:
		return b[:1], b[1:], false, nil
	case first <= 0xb7:
		l := int(first - 0x80)
		if len(b) < 1+l {
			return nil, nil, false, errors.New("rlp: str truncated")
		}
		return b[1 : 1+l], b[1+l:], false, nil
	case first <= 0xbf:
		ll := int(first - 0xb7)
		if len(b) < 1+ll {
			return nil, nil, false, errors.New("rlp: long str len truncated")
		}
		l := 0
		for i := 0; i < ll; i++ {
			l = l<<8 | int(b[1+i])
		}
		if len(b) < 1+ll+l {
			return nil, nil, false, errors.New("rlp: long str truncated")
		}
		return b[1+ll : 1+ll+l], b[1+ll+l:], false, nil
	case first <= 0xf7:
		l := int(first - 0xc0)
		if len(b) < 1+l {
			return nil, nil, false, errors.New("rlp: sub-list truncated")
		}
		return b[:1+l], b[1+l:], true, nil
	default:
		ll := int(first - 0xf7)
		if len(b) < 1+ll {
			return nil, nil, false, errors.New("rlp: long sub-list len truncated")
		}
		l := 0
		for i := 0; i < ll; i++ {
			l = l<<8 | int(b[1+i])
		}
		if len(b) < 1+ll+l {
			return nil, nil, false, errors.New("rlp: long sub-list truncated")
		}
		return b[:1+ll+l], b[1+ll+l:], true, nil
	}
}

// splitListItems fully splits a node's list into its item byte-slices,
// preserving for each whether it was a nested list (kept raw) or a string.
func splitListItems(node []byte) (items [][]byte, isList []bool, err error) {
	payload, _, err := rlpSplitList(node)
	if err != nil {
		return nil, nil, err
	}
	for len(payload) > 0 {
		c, rest, l, e := rlpSplitItem(payload)
		if e != nil {
			return nil, nil, e
		}
		items = append(items, c)
		isList = append(isList, l)
		payload = rest
	}
	return items, isList, nil
}
