package trie

import (
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
	Root Node
}

func NewInMemoryTrie(root ...Node) *Trie {
	t := &Trie{}
	if len(root) > 0 {
		t.Root = root[0]
	}
	return t
}

func (t *Trie) Reset() { t.Root = nil }

func (t *Trie) RootHash() []byte {
	if h, ok := t.Root.(*HashNode); ok {
		return h.Hash[:]
	}
	return nil
}

func MergeTries(tries []*Trie) (*Trie, error) {
	if len(tries) == 0 { return nil, nil }
	return tries[0], nil
}

func (t *Trie) GetAccount(key []byte) (*AccountNode, bool) { return nil, false }
func (t *Trie) Get(key []byte) (Node, bool) { return nil, false }
