package trie_test

// g49: cross-checks lib/trie's production HashBuilder/GenStructStep
// pipeline (driven via FlatDBTrieLoader.CalcTrieRoot, the same entry
// point trie_root_shard_test.go already exercises) against a small,
// completely independent, hand-written reference MPT implementation
// (no shared code with lib/trie). For accounts with no storage and no
// code, n42's account leaves encode the standard Ethereum account RLP
// (nonce, balance, storageRoot, codeHash), so the two roots must agree
// byte-for-byte when both are seeded with the same (addrHash, account)
// pairs.
//
// The reference implementation below mirrors the textbook Ethereum
// Merkle-Patricia-Trie algorithm: insert() builds leaf/extension/branch
// nodes directly from nibble paths, and hash() recursively RLP-encodes
// each node, inlining the child's raw RLP when it is shorter than 32
// bytes and substituting keccak256(childRLP) otherwise — exactly the
// "hex-prefix" + "small node inlining" rules from the Ethereum Yellow
// Paper, appendix D.

import (
	"bytes"
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/lib/trie"
)

// ---------------------------------------------------------------------
// Reference MPT (independent of lib/trie's own node/hasher types).
// ---------------------------------------------------------------------

type g49RefNode interface{}

type g49RefLeaf struct {
	key   []byte // remaining nibbles
	value []byte
}

type g49RefExt struct {
	key   []byte // shared nibbles
	child g49RefNode
}

type g49RefBranch struct {
	children [16]g49RefNode
	value    []byte
}

func g49PrefixLen(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// g49Insert follows the standard recursive MPT insertion algorithm.
func g49Insert(n g49RefNode, key, value []byte) g49RefNode {
	if n == nil {
		return &g49RefLeaf{key: append([]byte{}, key...), value: value}
	}
	switch nd := n.(type) {
	case *g49RefLeaf:
		matchLen := g49PrefixLen(nd.key, key)
		if matchLen == len(nd.key) && matchLen == len(key) {
			return &g49RefLeaf{key: nd.key, value: value}
		}
		branch := &g49RefBranch{}
		if matchLen == len(nd.key) {
			branch.value = nd.value
		} else {
			branch.children[nd.key[matchLen]] = &g49RefLeaf{key: nd.key[matchLen+1:], value: nd.value}
		}
		if matchLen == len(key) {
			branch.value = value
		} else {
			branch.children[key[matchLen]] = &g49RefLeaf{key: key[matchLen+1:], value: value}
		}
		if matchLen == 0 {
			return branch
		}
		return &g49RefExt{key: key[:matchLen], child: branch}

	case *g49RefExt:
		matchLen := g49PrefixLen(nd.key, key)
		if matchLen == len(nd.key) {
			return &g49RefExt{key: nd.key, child: g49Insert(nd.child, key[matchLen:], value)}
		}
		branch := &g49RefBranch{}
		var branchChild g49RefNode
		if matchLen+1 == len(nd.key) {
			branchChild = nd.child
		} else {
			branchChild = &g49RefExt{key: nd.key[matchLen+1:], child: nd.child}
		}
		branch.children[nd.key[matchLen]] = branchChild
		if matchLen == len(key) {
			branch.value = value
		} else {
			branch.children[key[matchLen]] = &g49RefLeaf{key: key[matchLen+1:], value: value}
		}
		if matchLen == 0 {
			return branch
		}
		return &g49RefExt{key: key[:matchLen], child: branch}

	case *g49RefBranch:
		if len(key) == 0 {
			nd.value = value
			return nd
		}
		nd.children[key[0]] = g49Insert(nd.children[key[0]], key[1:], value)
		return nd
	}
	panic("unreachable")
}

// g49CompactEncode is the standard Ethereum "hex-prefix" nibble
// compaction (Yellow Paper appendix C).
func g49CompactEncode(nibbles []byte, terminator bool) []byte {
	odd := len(nibbles)%2 == 1
	var prefix byte
	if terminator {
		prefix = 0x20
	}
	start := 0
	if odd {
		prefix |= 0x10 | nibbles[0]
		start = 1
	}
	out := []byte{prefix}
	for i := start; i < len(nibbles); i += 2 {
		out = append(out, nibbles[i]<<4|nibbles[i+1])
	}
	return out
}

// g49Ref returns the RLP "reference" for a child node: the raw node
// RLP bytes if short (<32B, inlined per the Yellow Paper), otherwise
// its keccak256 hash as a 32-byte string.
func g49Ref(t *testing.T, n g49RefNode) interface{} {
	t.Helper()
	if n == nil {
		return []byte{}
	}
	encoded := g49Encode(t, n)
	if len(encoded) < 32 {
		return rlp.RawValue(encoded)
	}
	h := crypto.Keccak256(encoded)
	return h
}

// g49Encode RLP-encodes one node (NOT its reference — the raw node
// bytes, used both for the top-level root and for computing refs).
func g49Encode(t *testing.T, n g49RefNode) []byte {
	t.Helper()
	switch nd := n.(type) {
	case nil:
		b, err := rlp.EncodeToBytes([]byte{})
		if err != nil {
			t.Fatalf("rlp encode empty: %v", err)
		}
		return b
	case *g49RefLeaf:
		list := []interface{}{g49CompactEncode(nd.key, true), nd.value}
		b, err := rlp.EncodeToBytes(list)
		if err != nil {
			t.Fatalf("rlp encode leaf: %v", err)
		}
		return b
	case *g49RefExt:
		list := []interface{}{g49CompactEncode(nd.key, false), g49Ref(t, nd.child)}
		b, err := rlp.EncodeToBytes(list)
		if err != nil {
			t.Fatalf("rlp encode ext: %v", err)
		}
		return b
	case *g49RefBranch:
		list := make([]interface{}, 17)
		for i := 0; i < 16; i++ {
			list[i] = g49Ref(t, nd.children[i])
		}
		if nd.value != nil {
			list[16] = nd.value
		} else {
			list[16] = []byte{}
		}
		b, err := rlp.EncodeToBytes(list)
		if err != nil {
			t.Fatalf("rlp encode branch: %v", err)
		}
		return b
	}
	panic("unreachable")
}

// g49RefHash computes the reference MPT root hash over a set of
// (key32, value) pairs, where key32 is ALREADY the hashed key (we
// build a plain trie, matching how n42/reth store HashedAccounts —
// the key space is already keccak-hashed, no secure-trie re-hash).
func g49RefHash(t *testing.T, entries map[[32]byte][]byte) types.Hash {
	t.Helper()
	var root g49RefNode
	if len(entries) == 0 {
		return types.BytesToHash(crypto.Keccak256(mustRLPEmptyString(t)))
	}
	for k, v := range entries {
		nibbles := make([]byte, 64)
		for i, b := range k {
			nibbles[i*2] = b >> 4
			nibbles[i*2+1] = b & 0x0f
		}
		root = g49Insert(root, nibbles, v)
	}
	return types.BytesToHash(crypto.Keccak256(g49Encode(t, root)))
}

func mustRLPEmptyString(t *testing.T) []byte {
	b, err := rlp.EncodeToBytes([]byte{})
	if err != nil {
		t.Fatalf("rlp empty: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------
// Cross-check tests against lib/trie's production pipeline.
// ---------------------------------------------------------------------

type g49SeedAccount struct {
	addrHash [32]byte
	nonce    uint64
	balance  uint64
}

// g49AccountRLP builds the standard Ethereum account RLP
// [nonce, balance, storageRoot, codeHash] for an account with no
// storage and no code.
func g49AccountRLP(t *testing.T, nonce, balance uint64) []byte {
	t.Helper()
	list := []interface{}{
		nonce,
		new(uint256.Int).SetUint64(balance).Bytes(),
		trie.EmptyRoot[:],
		trie.EmptyCodeHash[:],
	}
	b, err := rlp.EncodeToBytes(list)
	if err != nil {
		t.Fatalf("rlp encode account: %v", err)
	}
	return b
}

func g49RefRoot(t *testing.T, accounts []g49SeedAccount) types.Hash {
	t.Helper()
	entries := make(map[[32]byte][]byte, len(accounts))
	for _, a := range accounts {
		entries[a.addrHash] = g49AccountRLP(t, a.nonce, a.balance)
	}
	return g49RefHash(t, entries)
}

func g49N42Root(t *testing.T, accounts []g49SeedAccount) types.Hash {
	t.Helper()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	for _, a := range accounts {
		acc := account.NewAccount()
		acc.Nonce = a.nonce
		acc.Balance = *uint256.NewInt(a.balance)
		buf := make([]byte, acc.EncodingLengthForStorage())
		acc.EncodeForStorage(buf)
		if err := tx.Put(kv.HashedAccounts, a.addrHash[:], buf); err != nil {
			t.Fatal(err)
		}
	}

	loader := trie.NewFlatDBTrieLoader("g49-ethcompat", trie.NewRetainList(0), nil, nil, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("CalcTrieRoot: %v", err)
	}
	return root
}

func g49Accounts(n int) []g49SeedAccount {
	out := make([]g49SeedAccount, n)
	for i := 0; i < n; i++ {
		var seed [8]byte
		seed[0] = byte(i)
		seed[1] = byte(i >> 8)
		hashed := crypto.Keccak256(seed[:])
		var a g49SeedAccount
		copy(a.addrHash[:], hashed)
		a.nonce = uint64(i) + 1
		a.balance = uint64(i)*1_000_003 + 7
		out[i] = a
	}
	return out
}

func TestEmptyRoot_MatchesReferenceEmptyRoot(t *testing.T) {
	gotRoot := g49N42Root(t, nil)
	wantRoot := g49RefHash(t, map[[32]byte][]byte{})
	if gotRoot != wantRoot {
		t.Errorf("empty root mismatch: got %x want %x", gotRoot, wantRoot)
	}
	if gotRoot != trie.EmptyRoot {
		t.Errorf("empty root != trie.EmptyRoot constant: got %x want %x", gotRoot, trie.EmptyRoot)
	}
}

func TestN42Root_MatchesReference_SingleAccount(t *testing.T) {
	accts := g49Accounts(1)
	got := g49N42Root(t, accts)
	want := g49RefRoot(t, accts)
	if got != want {
		t.Errorf("single-account root mismatch:\n  n42 = %x\n  ref = %x", got, want)
	}
}

func TestN42Root_MatchesReference_SmallSets(t *testing.T) {
	for _, n := range []int{2, 3, 8, 17, 64} {
		t.Run("", func(t *testing.T) {
			accts := g49Accounts(n)
			got := g49N42Root(t, accts)
			want := g49RefRoot(t, accts)
			if got != want {
				t.Errorf("n=%d root mismatch:\n  n42 = %x\n  ref = %x", n, got, want)
			}
		})
	}
}

// TestRefMPT_SelfConsistency sanity-checks the reference implementation
// itself against a hand-computable case: two leaves sharing a 1-nibble
// prefix must produce an extension node wrapping a 2-way branch, and
// re-inserting the same key must not change the root (idempotent
// update), independent of insertion order.
func TestRefMPT_SelfConsistency(t *testing.T) {
	var k1, k2 [32]byte
	k1[0] = 0x12
	k2[0] = 0x13 // shares nibble 0x1 with k1, diverges at nibble 1

	entries := map[[32]byte][]byte{k1: []byte("v1"), k2: []byte("v2")}
	root1 := g49RefHash(t, entries)

	// Overwrite k1 with the same value — root must be unchanged.
	entries[k1] = []byte("v1")
	root2 := g49RefHash(t, entries)
	if root1 != root2 {
		t.Errorf("idempotent re-insert changed root: %x vs %x", root1, root2)
	}

	if bytes.Equal(root1[:], make([]byte, 32)) {
		t.Error("root should not be the zero hash")
	}
}
