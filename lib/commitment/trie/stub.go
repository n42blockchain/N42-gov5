package trie

import (
	"bytes"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

var EmptyRoot = types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

type Node interface{ foldable() }

type FullNode struct{ Children [17]Node }
func (f *FullNode) foldable() {}

type ShortNode struct {
	Key []byte
	Val Node
}
func (s *ShortNode) foldable() {}

type HashNode struct{ Hash types.Hash }
func (h *HashNode) foldable() {}
func NewHashNode(hash types.Hash) *HashNode { return &HashNode{Hash: hash} }

type AccountNode struct {
	Account     account.StateAccount
	Storage     Node
	RootCorrect bool
	Code        []byte
	CodeSize    int
}
func (a *AccountNode) foldable() {}

type ValueNode []byte
func (v ValueNode) foldable() {}

type Trie struct {
	Root     Node
	roots    []Node
	rootHash []byte
}

func NewInMemoryTrie(root ...Node) *Trie {
	t := &Trie{}
	if len(root) > 0 {
		t.Root = root[0]
		if root[0] != nil {
			t.roots = []Node{root[0]}
		}
	}
	return t
}

// Reset mirrors the production trie API where reset clears cached hash refs,
// but must not discard the proof structure itself.
func (t *Trie) Reset() {}

func (t *Trie) RootHash() []byte {
	if len(t.rootHash) > 0 {
		return append([]byte(nil), t.rootHash...)
	}
	if h, ok := t.Root.(*HashNode); ok {
		return append([]byte(nil), h.Hash[:]...)
	}
	return nil
}

func MergeTries(tries []*Trie) (*Trie, error) {
	if len(tries) == 0 {
		return nil, nil
	}
	merged := &Trie{}
	for _, tr := range tries {
		if tr == nil {
			continue
		}
		if len(merged.rootHash) == 0 && len(tr.rootHash) > 0 {
			merged.rootHash = append([]byte(nil), tr.rootHash...)
		}
		for _, root := range tr.proofRoots() {
			if root != nil {
				merged.roots = append(merged.roots, root)
			}
		}
	}
	if len(merged.roots) == 1 {
		merged.Root = merged.roots[0]
	}
	if len(merged.roots) == 0 {
		return nil, nil
	}
	return merged, nil
}

func (t *Trie) SetRootHash(rootHash []byte) {
	if t == nil {
		return
	}
	t.rootHash = append(t.rootHash[:0], rootHash...)
}

func (t *Trie) GetAccount(key []byte) (*AccountNode, bool) {
	if t == nil {
		return nil, false
	}
	nibbles := bytesToNibbles(key)
	foundAbsence := false
	for _, root := range t.proofRoots() {
		node, status := lookup(root, nibbles)
		switch status {
		case lookupPresent:
			acc, ok := node.(*AccountNode)
			if ok {
				return acc, true
			}
		case lookupAbsent:
			foundAbsence = true
		}
	}
	if foundAbsence {
		return nil, true
	}
	return nil, false
}

func (t *Trie) Get(key []byte) (Node, bool) {
	if t == nil {
		return nil, false
	}
	nibbles := bytesToNibbles(key)
	foundAbsence := false
	for _, root := range t.proofRoots() {
		node, status := lookup(root, nibbles)
		switch status {
		case lookupPresent:
			switch node.(type) {
			case *AccountNode:
				continue
			default:
				return node, true
			}
		case lookupAbsent:
			foundAbsence = true
		}
	}
	if foundAbsence {
		return nil, true
	}
	return nil, false
}

func (t *Trie) proofRoots() []Node {
	if len(t.roots) > 0 {
		return t.roots
	}
	if t.Root != nil {
		return []Node{t.Root}
	}
	return nil
}

type lookupStatus uint8

const (
	lookupUnknown lookupStatus = iota
	lookupAbsent
	lookupPresent
)

func lookup(node Node, nibbles []byte) (Node, lookupStatus) {
	switch n := node.(type) {
	case nil:
		return nil, lookupAbsent
	case *HashNode:
		return nil, lookupUnknown
	case *FullNode:
		if len(nibbles) == 0 {
			if n.Children[16] == nil {
				return nil, lookupAbsent
			}
			return lookup(n.Children[16], nil)
		}
		child := n.Children[nibbles[0]]
		if child == nil {
			return nil, lookupAbsent
		}
		return lookup(child, nibbles[1:])
	case *ShortNode:
		key := n.Key
		if len(key) > 0 && key[len(key)-1] == 16 {
			key = key[:len(key)-1]
		}
		if len(nibbles) < len(key) {
			if bytes.Equal(key[:len(nibbles)], nibbles) {
				return nil, lookupAbsent
			}
			return nil, lookupAbsent
		}
		if !bytes.Equal(key, nibbles[:len(key)]) {
			return nil, lookupAbsent
		}
		if n.Val == nil {
			return nil, lookupAbsent
		}
		return lookup(n.Val, nibbles[len(key):])
	case *AccountNode:
		if len(nibbles) == 0 {
			return n, lookupPresent
		}
		if n.Storage == nil {
			return nil, lookupAbsent
		}
		return lookup(n.Storage, nibbles)
	case ValueNode:
		if len(nibbles) == 0 {
			return n, lookupPresent
		}
		return nil, lookupAbsent
	case *ValueNode:
		if len(nibbles) == 0 {
			return n, lookupPresent
		}
		return nil, lookupAbsent
	default:
		return nil, lookupUnknown
	}
}

func bytesToNibbles(key []byte) []byte {
	nibbles := make([]byte, len(key)*2)
	for i, b := range key {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0f
	}
	return nibbles
}
