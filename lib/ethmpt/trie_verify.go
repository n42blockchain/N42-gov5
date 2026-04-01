// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The helper functions in this file are adapted from the go-ethereum trie
// verifier so canonical MPT proof validation can run without linking against
// geth's crypto/secp256k1 implementation.

package ethmpt

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

type trieNode interface{}

type (
	fullNode struct {
		Children [17]trieNode
	}
	shortNode struct {
		Key []byte
		Val trieNode
	}
	hashNode  []byte
	valueNode []byte
)

const trieHashLen = types.HashLength

func verifyProof(root types.Hash, key []byte, proof [][]byte) ([]byte, error) {
	proofDB := make(map[types.Hash][]byte, len(proof))
	for i, node := range proof {
		if len(node) == 0 {
			return nil, fmt.Errorf("empty proof node %d", i)
		}
		proofDB[keccak256Hash(node)] = node
	}
	key = keybytesToHex(key)
	wantHash := root
	for i := 0; ; i++ {
		buf, ok := proofDB[wantHash]
		if !ok {
			return nil, fmt.Errorf("proof node %d (hash %064x) missing", i, wantHash)
		}
		n, err := decodeNode(wantHash[:], buf)
		if err != nil {
			return nil, fmt.Errorf("bad proof node %d: %v", i, err)
		}
		keyrest, cld := get(n, key, true)
		switch cld := cld.(type) {
		case nil:
			return nil, nil
		case hashNode:
			key = keyrest
			copy(wantHash[:], cld)
		case valueNode:
			return cld, nil
		default:
			return nil, fmt.Errorf("unexpected trie node type %T", cld)
		}
	}
}

func keybytesToHex(str []byte) []byte {
	l := len(str)*2 + 1
	nibbles := make([]byte, l)
	for i, b := range str {
		nibbles[i*2] = b / 16
		nibbles[i*2+1] = b % 16
	}
	nibbles[l-1] = 16
	return nibbles
}

func compactToHex(compact []byte) []byte {
	if len(compact) == 0 {
		return compact
	}
	base := keybytesToHex(compact)
	if base[0] < 2 {
		base = base[:len(base)-1]
	}
	chop := 2 - base[0]&1
	return base[chop:]
}

func hasTerm(s []byte) bool {
	return len(s) > 0 && s[len(s)-1] == 16
}

func decodeNode(hash, buf []byte) (trieNode, error) {
	return decodeNodeUnsafe(hash, types.CopyBytes(buf))
}

func decodeNodeUnsafe(hash, buf []byte) (trieNode, error) {
	if len(buf) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	elems, _, err := rlp.SplitList(buf)
	if err != nil {
		return nil, fmt.Errorf("decode error: %v", err)
	}
	count, err := rlp.CountValues(elems)
	if err != nil {
		return nil, err
	}
	switch count {
	case 2:
		n, err := decodeShort(hash, elems)
		return n, wrapError(err, "short")
	case 17:
		n, err := decodeFull(hash, elems)
		return n, wrapError(err, "full")
	default:
		return nil, fmt.Errorf("invalid number of list elements: %v", count)
	}
}

func decodeShort(hash, elems []byte) (trieNode, error) {
	kbuf, rest, err := rlp.SplitString(elems)
	if err != nil {
		return nil, err
	}
	key := compactToHex(kbuf)
	if hasTerm(key) {
		val, _, err := rlp.SplitString(rest)
		if err != nil {
			return nil, fmt.Errorf("invalid value node: %v", err)
		}
		return &shortNode{Key: key, Val: valueNode(val)}, nil
	}
	r, _, err := decodeRef(rest)
	if err != nil {
		return nil, wrapError(err, "val")
	}
	return &shortNode{Key: key, Val: r}, nil
}

func decodeFull(hash, elems []byte) (*fullNode, error) {
	n := &fullNode{}
	for i := 0; i < 16; i++ {
		cld, rest, err := decodeRef(elems)
		if err != nil {
			return n, wrapError(err, fmt.Sprintf("[%d]", i))
		}
		n.Children[i], elems = cld, rest
	}
	val, _, err := rlp.SplitString(elems)
	if err != nil {
		return n, err
	}
	if len(val) > 0 {
		n.Children[16] = valueNode(val)
	}
	return n, nil
}

func decodeRef(buf []byte) (trieNode, []byte, error) {
	kind, val, rest, err := rlp.Split(buf)
	if err != nil {
		return nil, buf, err
	}
	switch {
	case kind == rlp.List:
		if size := len(buf) - len(rest); size > trieHashLen {
			return nil, buf, fmt.Errorf("oversized embedded node (size is %d bytes, want size < %d)", size, trieHashLen)
		}
		n, err := decodeNodeUnsafe(nil, buf[:len(buf)-len(rest)])
		return n, rest, err
	case kind == rlp.String && len(val) == 0:
		return nil, rest, nil
	case kind == rlp.String && len(val) == trieHashLen:
		return hashNode(val), rest, nil
	default:
		return nil, nil, fmt.Errorf("invalid RLP string size %d (want 0 or %d)", len(val), trieHashLen)
	}
}

type decodeError struct {
	what  error
	stack []string
}

func wrapError(err error, ctx string) error {
	if err == nil {
		return nil
	}
	if decErr, ok := err.(*decodeError); ok {
		decErr.stack = append(decErr.stack, ctx)
		return decErr
	}
	return &decodeError{what: err, stack: []string{ctx}}
}

func (err *decodeError) Error() string {
	return fmt.Sprintf("%v (decode path: %s)", err.what, strings.Join(err.stack, "<-"))
}

func get(tn trieNode, key []byte, skipResolved bool) ([]byte, trieNode) {
	for {
		switch n := tn.(type) {
		case *shortNode:
			if !bytes.HasPrefix(key, n.Key) {
				return nil, nil
			}
			tn = n.Val
			key = key[len(n.Key):]
			if !skipResolved {
				return key, tn
			}
		case *fullNode:
			tn = n.Children[key[0]]
			key = key[1:]
			if !skipResolved {
				return key, tn
			}
		case hashNode:
			return key, n
		case nil:
			return key, nil
		case valueNode:
			return nil, n
		default:
			panic(fmt.Sprintf("%T: invalid node: %v", tn, tn))
		}
	}
}
