package trie

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func TestBytesToNibbles(t *testing.T) {
	got := bytesToNibbles([]byte{0x12, 0xAB})
	want := []byte{0x1, 0x2, 0xA, 0xB}
	if !bytes.Equal(got, want) {
		t.Fatalf("bytesToNibbles = %v, want %v", got, want)
	}
}

func TestNewHashNodeAndFoldable(t *testing.T) {
	h := types.HexToHash("0x01")
	n := NewHashNode(h)
	if n.Hash != h {
		t.Fatalf("NewHashNode hash mismatch")
	}
	// Exercise the foldable marker methods on every Node implementation.
	var nodes = []Node{
		&FullNode{},
		&ShortNode{},
		n,
		&AccountNode{},
		ValueNode{},
	}
	for _, nd := range nodes {
		nd.foldable()
	}
}

func TestTrieResetIsNoOp(t *testing.T) {
	tr := NewInMemoryTrie()
	tr.Reset() // must not panic
}

func TestTrieRootHashFromCachedValue(t *testing.T) {
	tr := NewInMemoryTrie()
	tr.SetRootHash([]byte{1, 2, 3})
	got := tr.RootHash()
	if !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("RootHash() = %v, want [1 2 3]", got)
	}
	// Returned slice must be a copy.
	got[0] = 0xFF
	if tr.rootHash[0] == 0xFF {
		t.Fatal("RootHash() must return a defensive copy")
	}
}

func TestTrieRootHashFromHashNode(t *testing.T) {
	h := types.HexToHash("0xabc")
	tr := NewInMemoryTrie(NewHashNode(h))
	got := tr.RootHash()
	if !bytes.Equal(got, h[:]) {
		t.Fatalf("RootHash() = %x, want %x", got, h[:])
	}
}

func TestTrieRootHashNilRoot(t *testing.T) {
	tr := NewInMemoryTrie()
	if got := tr.RootHash(); got != nil {
		t.Fatalf("RootHash() on empty trie = %v, want nil", got)
	}
}

func TestTrieSetRootHashOnNilReceiver(t *testing.T) {
	var tr *Trie
	tr.SetRootHash([]byte{1}) // must not panic
}

func TestTrieGetAndGetAccountOnNilReceiver(t *testing.T) {
	var tr *Trie
	if _, ok := tr.Get([]byte("x")); ok {
		t.Fatal("Get on nil trie should report not found")
	}
	if _, ok := tr.GetAccount([]byte("x")); ok {
		t.Fatal("GetAccount on nil trie should report not found")
	}
}

func TestTrieGetAccountPresent(t *testing.T) {
	key := []byte{0x12, 0x34}
	nib := keybytesToHex(key)
	acc := &AccountNode{Account: account.StateAccount{Nonce: 7}}
	leaf := &ShortNode{Key: nib, Val: acc}
	tr := NewInMemoryTrie(leaf)

	got, ok := tr.GetAccount(key)
	if !ok || got == nil || got.Account.Nonce != 7 {
		t.Fatalf("GetAccount = (%v, %v), want account with nonce 7", got, ok)
	}
}

func TestTrieGetAccountAbsent(t *testing.T) {
	key := []byte{0x12, 0x34}
	otherKey := []byte{0x56, 0x78}
	nib := keybytesToHex(otherKey)
	leaf := &ShortNode{Key: nib, Val: &AccountNode{}}
	tr := NewInMemoryTrie(leaf)

	got, ok := tr.GetAccount(key)
	// A divergent key is a proven absence: ok=true (the proof conclusively
	// shows no account exists), but the account itself is nil.
	if !ok || got != nil {
		t.Fatalf("GetAccount(divergent key) = (%v, %v), want (nil, true)", got, ok)
	}
}

func TestTrieGetValueSkipsAccountNodes(t *testing.T) {
	key := []byte{0x01}
	nib := keybytesToHex(key)
	leaf := &ShortNode{Key: nib, Val: &AccountNode{}}
	tr := NewInMemoryTrie(leaf)

	// Get() should skip AccountNode matches (it wants plain values), so this
	// should report "not found" since the only match is an AccountNode.
	if _, ok := tr.Get(key); ok {
		t.Fatal("Get() should not return an AccountNode as a plain value")
	}
}

func TestTrieGetValuePresent(t *testing.T) {
	key := []byte{0x01}
	nib := keybytesToHex(key)
	leaf := &ShortNode{Key: nib, Val: ValueNode("hello")}
	tr := NewInMemoryTrie(leaf)

	got, ok := tr.Get(key)
	if !ok {
		t.Fatal("expected Get to find the value")
	}
	vn, isVN := got.(ValueNode)
	if !isVN || string(vn) != "hello" {
		t.Fatalf("Get() = %v, want ValueNode(hello)", got)
	}
}

func TestLookupHashNodeIsUnknown(t *testing.T) {
	node, status := lookup(&HashNode{}, []byte{1})
	if status != lookupUnknown || node != nil {
		t.Fatalf("lookup(HashNode) = (%v, %v), want (nil, lookupUnknown)", node, status)
	}
}

func TestLookupNilNodeIsAbsent(t *testing.T) {
	node, status := lookup(nil, []byte{1})
	if status != lookupAbsent || node != nil {
		t.Fatalf("lookup(nil) = (%v, %v), want (nil, lookupAbsent)", node, status)
	}
}

func TestLookupFullNodeTerminator(t *testing.T) {
	fn := &FullNode{}
	fn.Children[16] = ValueNode("term")
	node, status := lookup(fn, nil)
	if status != lookupPresent {
		t.Fatalf("lookup(FullNode terminator) status = %v, want lookupPresent", status)
	}
	if vn, ok := node.(ValueNode); !ok || string(vn) != "term" {
		t.Fatalf("lookup(FullNode terminator) node = %v", node)
	}
}

func TestLookupFullNodeMissingTerminator(t *testing.T) {
	fn := &FullNode{}
	_, status := lookup(fn, nil)
	if status != lookupAbsent {
		t.Fatalf("lookup(FullNode, no terminator) status = %v, want lookupAbsent", status)
	}
}

func TestLookupFullNodeMissingChild(t *testing.T) {
	fn := &FullNode{}
	_, status := lookup(fn, []byte{5})
	if status != lookupAbsent {
		t.Fatalf("lookup(FullNode, missing child) status = %v, want lookupAbsent", status)
	}
}

func TestLookupShortNodeKeyTooLong(t *testing.T) {
	sn := &ShortNode{Key: []byte{1, 2, 3}, Val: ValueNode("v")}
	_, status := lookup(sn, []byte{1})
	if status != lookupAbsent {
		t.Fatalf("lookup(ShortNode, short nibbles) status = %v, want lookupAbsent", status)
	}
}

func TestLookupShortNodeKeyMismatch(t *testing.T) {
	sn := &ShortNode{Key: []byte{1, 2}, Val: ValueNode("v")}
	_, status := lookup(sn, []byte{9, 9})
	if status != lookupAbsent {
		t.Fatalf("lookup(ShortNode, mismatched key) status = %v, want lookupAbsent", status)
	}
}

func TestLookupShortNodeNilVal(t *testing.T) {
	sn := &ShortNode{Key: []byte{1, 2}, Val: nil}
	_, status := lookup(sn, []byte{1, 2})
	if status != lookupAbsent {
		t.Fatalf("lookup(ShortNode, nil val) status = %v, want lookupAbsent", status)
	}
}

func TestLookupAccountNodeStorage(t *testing.T) {
	storageLeaf := &ShortNode{Key: []byte{3, 4, 16}, Val: ValueNode("slot")}
	acc := &AccountNode{Storage: storageLeaf}

	// Non-empty remaining nibbles routes into storage.
	node, status := lookup(acc, []byte{3, 4})
	if status != lookupPresent {
		t.Fatalf("lookup(AccountNode->Storage) status = %v, want lookupPresent", status)
	}
	if vn, ok := node.(ValueNode); !ok || string(vn) != "slot" {
		t.Fatalf("lookup(AccountNode->Storage) node = %v", node)
	}
}

func TestLookupAccountNodeNoStorage(t *testing.T) {
	acc := &AccountNode{}
	_, status := lookup(acc, []byte{1})
	if status != lookupAbsent {
		t.Fatalf("lookup(AccountNode, nil storage) status = %v, want lookupAbsent", status)
	}
}

func TestLookupValueNodeVariants(t *testing.T) {
	if _, status := lookup(ValueNode("v"), nil); status != lookupPresent {
		t.Fatalf("lookup(ValueNode, empty nibbles) status = %v, want lookupPresent", status)
	}
	if _, status := lookup(ValueNode("v"), []byte{1}); status != lookupAbsent {
		t.Fatalf("lookup(ValueNode, nonempty nibbles) status = %v, want lookupAbsent", status)
	}
	vn := ValueNode("v")
	if _, status := lookup(&vn, nil); status != lookupPresent {
		t.Fatalf("lookup(*ValueNode, empty nibbles) status = %v, want lookupPresent", status)
	}
	if _, status := lookup(&vn, []byte{1}); status != lookupAbsent {
		t.Fatalf("lookup(*ValueNode, nonempty nibbles) status = %v, want lookupAbsent", status)
	}
}

func TestLookupDefaultUnknownType(t *testing.T) {
	_, status := lookup(struct{ Node }{}, nil)
	if status != lookupUnknown {
		t.Fatalf("lookup(unknown type) status = %v, want lookupUnknown", status)
	}
}

func TestMergeTriesEmpty(t *testing.T) {
	merged, err := MergeTries(nil)
	if err != nil || merged != nil {
		t.Fatalf("MergeTries(nil) = (%v, %v), want (nil, nil)", merged, err)
	}
}

func TestMergeTriesSkipsNilAndEmpty(t *testing.T) {
	merged, err := MergeTries([]*Trie{nil, {}})
	if err != nil {
		t.Fatalf("MergeTries error: %v", err)
	}
	if merged != nil {
		t.Fatalf("expected nil merge result when no roots present, got %v", merged)
	}
}

func TestMergeTriesSingleRoot(t *testing.T) {
	leaf := &ShortNode{Key: []byte{1}, Val: ValueNode("v")}
	tr := NewInMemoryTrie(leaf)

	merged, err := MergeTries([]*Trie{tr})
	if err != nil {
		t.Fatalf("MergeTries error: %v", err)
	}
	if merged == nil || merged.Root != leaf {
		t.Fatalf("expected merged.Root to be the single input root, got %v", merged)
	}
}

func TestMergeTriesMultipleRootsAndRootHash(t *testing.T) {
	leaf1 := &ShortNode{Key: []byte{1}, Val: ValueNode("a")}
	leaf2 := &ShortNode{Key: []byte{2}, Val: ValueNode("b")}
	tr1 := NewInMemoryTrie(leaf1)
	tr2 := NewInMemoryTrie(leaf2)
	tr2.SetRootHash([]byte{0xAA})

	merged, err := MergeTries([]*Trie{tr1, tr2})
	if err != nil {
		t.Fatalf("MergeTries error: %v", err)
	}
	if merged == nil {
		t.Fatal("expected a non-nil merged trie")
	}
	if len(merged.roots) != 2 {
		t.Fatalf("expected 2 merged roots, got %d", len(merged.roots))
	}
	// Multiple roots: merged.Root stays unset (only set when exactly 1 root).
	if merged.Root != nil {
		t.Fatalf("expected merged.Root to be nil with multiple roots, got %v", merged.Root)
	}
	if !bytes.Equal(merged.rootHash, []byte{0xAA}) {
		t.Fatalf("expected merged.rootHash to pick up tr2's cached hash, got %v", merged.rootHash)
	}
}
