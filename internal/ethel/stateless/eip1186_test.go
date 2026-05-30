package stateless

import (
	"bytes"
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// no-op HashCollectors for read-only proof extraction (TrieOf* already persisted
// by the preceding ComputeRoot; the proof pass only reads).
func nopAccHC(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
	return nil
}
func nopStorHC(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
	return nil
}

// genProof builds the production trie tables for (accts, stor) in a fresh memdb,
// computes the canonical root, then extracts a real EIP-1186 proof for `target`
// (+ slots) using the PRODUCTION ProofRetainer + FlatDBTrieLoader — the same
// machinery eth-el's header.Root uses. Returns (root, proofResult).
func genProof(t *testing.T,
	accts map[types.Address]*account.StateAccount,
	stor map[types.Address]map[types.Hash]*uint256.Int,
	target types.Address, slots []types.Hash,
) (types.Hash, *account.AccProofResult) {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	root, err := trc.ComputeRoot(accts, stor)
	if err != nil {
		t.Fatalf("ComputeRoot: %v", err)
	}

	rl := trie.NewRetainList(0)
	pr, err := trie.NewProofRetainer(target, accts[target], slots, rl)
	if err != nil {
		t.Fatalf("NewProofRetainer: %v", err)
	}
	loader := trie.NewFlatDBTrieLoader("getproof", rl, nopAccHC, nopStorHC, false)
	loader.SetProofRetainer(pr)
	got, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("CalcTrieRoot: %v", err)
	}
	if got != root {
		t.Fatalf("proof-pass root %x != ComputeRoot %x", got, root)
	}
	res, err := pr.ProofResult()
	if err != nil {
		t.Fatalf("ProofResult: %v", err)
	}
	return root, res
}

// TestEIP1186_AccountInclusion_EOAs proves P8's consumer ingests a REAL
// production-generated EIP-1186 account proof and anchors it to the canonical
// state root, recovering the account's fields byte-for-byte.
func TestEIP1186_AccountInclusion_EOAs(t *testing.T) {
	accts := map[types.Address]*account.StateAccount{}
	for i := 1; i <= 20; i++ {
		a := &account.StateAccount{}
		a.Reset()
		a.Nonce = uint64(i * 7)
		a.Balance.SetUint64(uint64(i) * 1_000_000_000)
		a.CodeHash = types.BytesToHash(emptyCodeHashBytes)
		a.Initialised = true
		accts[addr20(uint64(i))] = a
	}
	target := addr20(7)
	root, res := genProof(t, accts, nil, target, nil)

	va, err := VerifyAccountInclusion(root[:], res)
	if err != nil {
		t.Fatalf("VerifyAccountInclusion: %v", err)
	}
	if !va.Exists {
		t.Fatal("account should exist")
	}
	want := accts[target]
	if va.Nonce != want.Nonce {
		t.Fatalf("nonce %d != %d", va.Nonce, want.Nonce)
	}
	if va.Balance.Cmp(&want.Balance) != 0 {
		t.Fatalf("balance %s != %s", va.Balance.String(), want.Balance.String())
	}
	if !bytes.Equal(va.CodeHash, emptyCodeHashBytes) {
		t.Fatalf("codeHash %x != empty", va.CodeHash)
	}
	if !bytes.Equal(va.StorageRoot, emptyRootHash) {
		t.Fatalf("storageRoot %x != emptyRoot (EOA)", va.StorageRoot)
	}
}

// TestEIP1186_AccountInclusion_WithStorage proves the two-level case: a contract
// account proof plus per-slot storage proofs, all anchored to the canonical root
// / the account's storageRoot, verified through P8's partialTrie.
func TestEIP1186_AccountInclusion_WithStorage(t *testing.T) {
	accts := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]*uint256.Int{}

	// A handful of EOAs for trie shape, plus one contract with storage.
	for i := 1; i <= 10; i++ {
		a := &account.StateAccount{}
		a.Reset()
		a.Nonce = uint64(i)
		a.Balance.SetUint64(uint64(i) * 500)
		a.CodeHash = types.BytesToHash(emptyCodeHashBytes)
		a.Initialised = true
		accts[addr20(uint64(i))] = a
	}

	target := addr20(100)
	c := &account.StateAccount{}
	c.Reset()
	c.Nonce = 1
	c.Balance.SetUint64(42)
	c.CodeHash = types.BytesToHash([]byte{0xab, 0xcd})
	c.Initialised = true
	accts[target] = c

	slots := []types.Hash{slot32(1), slot32(2), slot32(99), slot32(12345)}
	m := map[types.Hash]*uint256.Int{}
	for i, s := range slots {
		m[s] = uint256.NewInt(uint64(i)*1000 + 7)
	}
	// also an extra slot not in the proven set, to give the subtree real shape
	m[slot32(777)] = uint256.NewInt(0xdeadbeef)
	stor[target] = m

	root, res := genProof(t, accts, stor, target, slots)

	va, err := VerifyAccountInclusion(root[:], res)
	if err != nil {
		t.Fatalf("VerifyAccountInclusion: %v", err)
	}
	if !va.Exists {
		t.Fatal("contract should exist")
	}
	if va.Nonce != c.Nonce || va.Balance.Cmp(&c.Balance) != 0 {
		t.Fatalf("account fields mismatch: nonce=%d bal=%s", va.Nonce, va.Balance.String())
	}
	if bytes.Equal(va.StorageRoot, emptyRootHash) {
		t.Fatal("contract storageRoot must be non-empty")
	}

	// Spot-check the proven slot values came back through the verifier.
	if len(res.StorageProof) != len(slots) {
		t.Fatalf("want %d storage proofs, got %d", len(slots), len(res.StorageProof))
	}
	for i, sp := range res.StorageProof {
		want := m[slots[i]].ToBig().String()
		if sp.Value != want {
			t.Fatalf("slot %d proof value %s != %s", i, sp.Value, want)
		}
	}
}
