// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers the JMTCommitment historical-proof surface left at 0%: Tree(),
// PrefetchPaths, SnapshotAt, GetAccountProofAt, and GetStorageProofAt. These
// back eth_getProof-style queries against a past JMT root, so the test proves
// an account and a storage slot at a frozen historical root, verifies both,
// then tampers with the proof and the root to show verification rejects both.

package commitment

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
)

func jmtTestAddr(b byte) types.Address {
	var a types.Address
	a[19] = b
	return a
}

func TestJMTCommitmentTree(t *testing.T) {
	tree := jmt.New(jmt.NewMemStore())
	c := NewJMTCommitment(tree)
	if c.Tree() != tree {
		t.Fatal("Tree() must return the exact tree instance passed to NewJMTCommitment")
	}
}

func TestJMTCommitmentPrefetchPaths(t *testing.T) {
	store := jmt.NewMemStore()
	c := NewJMTCommitment(jmt.New(store))

	addr1, addr2 := jmtTestAddr(1), jmtTestAddr(2)
	acct := &account.StateAccount{Initialised: true, Nonce: 1}
	acct.Balance.SetUint64(100)
	if err := c.UpdateAccount(addr1, acct); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateAccount(addr2, acct); err != nil {
		t.Fatal(err)
	}
	c.Root()
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	// PrefetchPaths is best-effort and swallows errors; it must not panic on
	// a mix of present and absent key hashes.
	present := AccountKeyHash(addr1)
	absent := AccountKeyHash(jmtTestAddr(99))
	c.PrefetchPaths([]jmt.Hash{present, absent})
}

func TestJMTCommitmentHistoricalProofs(t *testing.T) {
	store := jmt.NewMemStore()
	c := NewJMTCommitment(jmt.New(store))

	addr := jmtTestAddr(7)
	slot := types.Hash{31: 5}
	acct := &account.StateAccount{Initialised: true, Nonce: 3}
	acct.Balance.SetUint64(777)
	if err := c.UpdateAccount(addr, acct); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateStorage(addr, slot, uint256.NewInt(0xdead)); err != nil {
		t.Fatal(err)
	}
	root := c.Root()
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	// SnapshotAt on the empty root must fail.
	if _, err := c.SnapshotAt(types.Hash{}); err == nil {
		t.Fatal("SnapshotAt(EmptyHash) should error")
	}

	// Account proof at the historical root: valid inclusion, verifies, and a
	// tampered proof or root is rejected.
	acctProof, err := c.GetAccountProofAt(addr, root)
	if err != nil {
		t.Fatalf("GetAccountProofAt: %v", err)
	}
	if acctProof.Value == nil {
		t.Fatal("expected inclusion proof with a value for a known account")
	}
	hasher := jmt.DefaultHasher()
	val, err := jmt.VerifyProof(jmt.Hash(root), acctProof, hasher)
	if err != nil {
		t.Fatalf("VerifyProof(account): %v", err)
	}
	if string(val) != string(acctProof.Value) {
		t.Fatal("verified value mismatch")
	}

	// Tamper: flip a byte in the leaf node data -> verification must fail.
	tampered := *acctProof
	tampered.Path = append([]jmt.ProofEntry{}, acctProof.Path...)
	last := tampered.Path[len(tampered.Path)-1]
	badData := append([]byte{}, last.NodeData...)
	badData[0] ^= 0xff
	tampered.Path[len(tampered.Path)-1] = jmt.ProofEntry{NodeData: badData, Nibble: last.Nibble}
	if _, err := jmt.VerifyProof(jmt.Hash(root), &tampered, hasher); err == nil {
		t.Fatal("expected VerifyProof to reject a tampered leaf node")
	}

	// Tamper: verify against the wrong root -> must fail.
	wrongRoot := root
	wrongRoot[0] ^= 0xff
	if _, err := jmt.VerifyProof(jmt.Hash(wrongRoot), acctProof, hasher); err == nil {
		t.Fatal("expected VerifyProof to reject a mismatched root")
	}

	// Storage proof at the historical root.
	storProof, err := c.GetStorageProofAt(addr, slot, root)
	if err != nil {
		t.Fatalf("GetStorageProofAt: %v", err)
	}
	if storProof.Value == nil {
		t.Fatal("expected inclusion proof with a value for a known storage slot")
	}
	if _, err := jmt.VerifyProof(jmt.Hash(root), storProof, hasher); err != nil {
		t.Fatalf("VerifyProof(storage): %v", err)
	}

	// Exclusion proof: an address never written.
	exclProof, err := c.GetAccountProofAt(jmtTestAddr(200), root)
	if err != nil {
		t.Fatalf("GetAccountProofAt(missing): %v", err)
	}
	if exclProof.Value != nil {
		t.Fatal("expected exclusion proof (nil value) for an unwritten account")
	}
	if _, err := jmt.VerifyProof(jmt.Hash(root), exclProof, hasher); err != nil {
		t.Fatalf("VerifyProof(exclusion): %v", err)
	}

	// SnapshotAt with a bogus (non-empty, unknown) root must error rather
	// than silently returning an unusable tree -- or, if it defers the
	// error to first access, GetAccountProofAt through it must surface one.
	if _, err := c.GetAccountProofAt(addr, wrongRoot); err == nil {
		t.Fatal("expected an error proving against an unknown historical root")
	}
}
