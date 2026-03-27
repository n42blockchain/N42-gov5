package api

import (
	"bytes"
	"fmt"
	"io"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

type rawRLP []byte

func (r rawRLP) EncodeRLP(w io.Writer) error {
	_, err := w.Write(r)
	return err
}

type ethereumDerivableList interface {
	Len() int
	EncodeIndex(int, *bytes.Buffer)
}

type ethereumStackTrie struct {
	root *ethereumStackTrieNode
	last []byte
}

type ethereumStackTrieNode struct {
	typ      uint8
	key      []byte
	val      []byte
	children [16]*ethereumStackTrieNode
}

const (
	engineTrieEmptyNode = iota
	engineTrieBranchNode
	engineTrieExtNode
	engineTrieLeafNode
	engineTrieHashedNode
)

func newEthereumStackTrie() *ethereumStackTrie {
	return &ethereumStackTrie{root: &ethereumStackTrieNode{}}
}

func deriveEthereumListHash(list ethereumDerivableList) types.Hash {
	trie := newEthereumStackTrie()
	valueBuf := new(bytes.Buffer)
	var indexBuf []byte

	for i := 1; i < list.Len() && i <= 0x7f; i++ {
		indexBuf = rlp.AppendUint64(indexBuf[:0], uint64(i))
		if err := trie.Update(indexBuf, encodeEthereumDerivableValue(list, i, valueBuf)); err != nil {
			return types.Hash{}
		}
	}
	if list.Len() > 0 {
		indexBuf = rlp.AppendUint64(indexBuf[:0], 0)
		if err := trie.Update(indexBuf, encodeEthereumDerivableValue(list, 0, valueBuf)); err != nil {
			return types.Hash{}
		}
	}
	for i := 0x80; i < list.Len(); i++ {
		indexBuf = rlp.AppendUint64(indexBuf[:0], uint64(i))
		if err := trie.Update(indexBuf, encodeEthereumDerivableValue(list, i, valueBuf)); err != nil {
			return types.Hash{}
		}
	}
	return trie.Hash()
}

func encodeEthereumDerivableValue(list ethereumDerivableList, i int, buf *bytes.Buffer) []byte {
	buf.Reset()
	list.EncodeIndex(i, buf)
	return types.CopyBytes(buf.Bytes())
}

func (t *ethereumStackTrie) Update(key, value []byte) error {
	if len(value) == 0 {
		return fmt.Errorf("trying to insert empty value")
	}
	k := keybytesToHex(key)
	k = k[:len(k)-1]
	if bytes.Compare(t.last, k) >= 0 {
		return fmt.Errorf("non-ascending key order")
	}
	t.last = append(t.last[:0], k...)
	return t.insert(t.root, k, value)
}

func (t *ethereumStackTrie) insert(st *ethereumStackTrieNode, key, value []byte) error {
	switch st.typ {
	case engineTrieBranchNode:
		idx := int(key[0])
		for i := idx - 1; i >= 0; i-- {
			if st.children[i] != nil && st.children[i].typ != engineTrieHashedNode {
				t.hash(st.children[i], false)
				break
			}
		}
		if st.children[idx] == nil {
			st.children[idx] = &ethereumStackTrieNode{
				typ: engineTrieLeafNode,
				key: append([]byte(nil), key[1:]...),
				val: value,
			}
			return nil
		}
		return t.insert(st.children[idx], key[1:], value)
	case engineTrieExtNode:
		diffidx := diffIndex(st.key, key)
		if diffidx == len(st.key) {
			return t.insert(st.children[0], key[diffidx:], value)
		}
		var child *ethereumStackTrieNode
		if diffidx < len(st.key)-1 {
			child = &ethereumStackTrieNode{
				typ: engineTrieExtNode,
				key: append([]byte(nil), st.key[diffidx+1:]...),
			}
			child.children[0] = st.children[0]
			t.hash(child, false)
		} else {
			child = st.children[0]
			t.hash(child, false)
		}

		var branch *ethereumStackTrieNode
		if diffidx == 0 {
			st.typ = engineTrieBranchNode
			st.children[0] = nil
			branch = st
		} else {
			st.children[0] = &ethereumStackTrieNode{typ: engineTrieBranchNode}
			branch = st.children[0]
		}
		branch.children[st.key[diffidx]] = child
		branch.children[key[diffidx]] = &ethereumStackTrieNode{
			typ: engineTrieLeafNode,
			key: append([]byte(nil), key[diffidx+1:]...),
			val: value,
		}
		st.key = st.key[:diffidx]
	case engineTrieLeafNode:
		diffidx := diffIndex(st.key, key)
		if diffidx >= len(st.key) {
			return fmt.Errorf("trying to insert into existing key")
		}

		var branch *ethereumStackTrieNode
		if diffidx == 0 {
			st.typ = engineTrieBranchNode
			branch = st
		} else {
			st.typ = engineTrieExtNode
			st.children[0] = &ethereumStackTrieNode{typ: engineTrieBranchNode}
			branch = st.children[0]
		}
		branch.children[st.key[diffidx]] = &ethereumStackTrieNode{
			typ: engineTrieLeafNode,
			key: append([]byte(nil), st.key[diffidx+1:]...),
			val: st.val,
		}
		t.hash(branch.children[st.key[diffidx]], false)
		branch.children[key[diffidx]] = &ethereumStackTrieNode{
			typ: engineTrieLeafNode,
			key: append([]byte(nil), key[diffidx+1:]...),
			val: value,
		}
		st.key = st.key[:diffidx]
		st.val = nil
	case engineTrieEmptyNode:
		st.typ = engineTrieLeafNode
		st.key = append(st.key[:0], key...)
		st.val = value
	case engineTrieHashedNode:
		return fmt.Errorf("trying to insert into hash")
	default:
		return fmt.Errorf("invalid trie node type: %d", st.typ)
	}
	return nil
}

func (t *ethereumStackTrie) Hash() types.Hash {
	t.hash(t.root, true)
	return types.BytesToHash(t.root.val)
}

func (t *ethereumStackTrie) hash(st *ethereumStackTrieNode, isRoot bool) {
	var blob []byte

	switch st.typ {
	case engineTrieHashedNode:
		return
	case engineTrieEmptyNode:
		st.typ = engineTrieHashedNode
		st.val = hash.EmptyRootHash.Bytes()
		st.key = st.key[:0]
		return
	case engineTrieBranchNode:
		elems := make([]interface{}, 17)
		for i, child := range st.children {
			if child == nil {
				elems[i] = []byte(nil)
				continue
			}
			t.hash(child, false)
			elems[i] = childReference(child.val)
			st.children[i] = nil
		}
		elems[16] = []byte(nil)
		blob = mustEncodeRLP(elems)
	case engineTrieExtNode:
		t.hash(st.children[0], false)
		blob = mustEncodeRLP([]interface{}{
			hexToCompactInPlace(append([]byte(nil), st.key...)),
			childReference(st.children[0].val),
		})
		st.children[0] = nil
	case engineTrieLeafNode:
		key := append(append([]byte(nil), st.key...), byte(16))
		blob = mustEncodeRLP([]interface{}{
			hexToCompactInPlace(key),
			st.val,
		})
	default:
		panic("invalid trie node type")
	}

	st.typ = engineTrieHashedNode
	st.key = st.key[:0]
	if len(blob) < 32 && !isRoot {
		st.val = blob
		return
	}
	sum := crypto.Keccak256Hash(blob)
	st.val = sum.Bytes()
}

func childReference(val []byte) interface{} {
	if len(val) < 32 {
		return rawRLP(val)
	}
	return val
}

func mustEncodeRLP(v interface{}) []byte {
	enc, err := rlp.EncodeToBytes(v)
	if err != nil {
		panic(err)
	}
	return enc
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

func hexToCompactInPlace(hex []byte) []byte {
	hexLen := len(hex)
	firstByte := byte(0)
	if hexLen > 0 && hex[hexLen-1] == 16 {
		firstByte = 1 << 5
		hexLen--
	}
	binLen := hexLen/2 + 1
	ni := 0
	bi := 1
	if hexLen&1 == 1 {
		firstByte |= 1 << 4
		firstByte |= hex[0]
		ni++
	}
	for ; ni < hexLen; bi, ni = bi+1, ni+2 {
		hex[bi] = hex[ni]<<4 | hex[ni+1]
	}
	hex[0] = firstByte
	return hex[:binLen]
}

func diffIndex(a, b []byte) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}
