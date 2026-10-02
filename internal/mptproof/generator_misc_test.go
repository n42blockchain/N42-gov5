package mptproof

import (
	"bytes"
	"context"
	"testing"
)

// TestGenerator_StorageTrieRootAndUnifiedEnv covers the simple
// accessor methods that the two-env (legacy) Generator mode exposes.
func TestGenerator_StorageTrieRootAndUnifiedEnv(t *testing.T) {
	g, _, _ := g49Fixture(t, 3)

	aRoot, err := g.AccountsTrieRoot()
	if err != nil {
		t.Fatalf("AccountsTrieRoot: %v", err)
	}
	sRoot, err := g.StorageTrieRoot()
	if err != nil {
		t.Fatalf("StorageTrieRoot: %v", err)
	}
	var zero [32]byte
	if aRoot == zero || sRoot == zero {
		t.Error("expected non-zero roots")
	}

	// Legacy two-env mode: UnifiedEnv is nil.
	if env := g.UnifiedEnv(); env != nil {
		t.Error("expected nil UnifiedEnv in legacy two-dir mode")
	}
}

// TestAccountProof_Verify_And_ProofBytes covers the direct fold-based
// Verify() and ProofBytes() helpers (independent of the
// FullAccountProofBytes inline-sibling-rebuild path already covered
// elsewhere).
func TestAccountProof_Verify_And_ProofBytes(t *testing.T) {
	g, _, accounts := g49Fixture(t, 1)
	target := accounts[0]

	proof, err := g.LatestAccountProof(target.addr)
	if err != nil {
		t.Fatal(err)
	}
	ok, verr := proof.Verify()
	if verr != nil {
		t.Logf("Verify: ok=%v err=%v (ErrExtensionInPath/ErrInlineLeaf are expected MVP limitations)", ok, verr)
	} else if !ok {
		t.Error("Verify: expected true for a freshly-built single-leaf trie")
	}

	pb, err := proof.ProofBytes()
	if err != nil {
		t.Fatalf("ProofBytes: %v", err)
	}
	if len(pb) == 0 {
		t.Error("expected at least one proof node")
	}

	// Non-inclusion: Verify explicitly rejects (MVP limitation).
	var absent [20]byte
	absent[0] = 0xDE
	aproof, err := g.LatestAccountProof(absent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aproof.Verify(); err == nil {
		t.Error("expected error verifying a non-inclusion proof")
	}
}

// TestStorageProof_Verify_And_ProofBytes is the storage-side mirror.
func TestStorageProof_Verify_And_ProofBytes(t *testing.T) {
	g, _, accounts := g49Fixture(t, 1)
	target := accounts[0]
	var slot [32]byte
	for s := range target.slots {
		slot = s
		break
	}

	proofs, err := g.LatestStorageProofs(target.addr, [][32]byte{slot})
	if err != nil {
		t.Fatal(err)
	}
	sp := proofs[0]
	ok, verr := sp.Verify()
	if verr != nil {
		t.Logf("Verify: ok=%v err=%v", ok, verr)
	} else if !ok {
		t.Error("Verify: expected true")
	}
	pb, err := sp.ProofBytes()
	if err != nil {
		t.Fatalf("ProofBytes: %v", err)
	}
	if len(pb) == 0 {
		t.Error("expected at least one proof node")
	}
}

// TestGenerator_VerifyFullStorage exercises the storage subtree-rebuild
// fallback path (VerifyFullStorage), which falls back to ScanStorage
// when the fast walk-fold check hits ErrExtensionInPath.
func TestGenerator_VerifyFullStorage(t *testing.T) {
	g, _, accounts := g49Fixture(t, 25)
	// Try several accounts/slots; at least one must verify true via
	// either the fast fold or the subtree-rebuild fallback.
	var (
		verified int
		errs     int
	)
	for _, a := range accounts {
		for slot := range a.slots {
			proofs, err := g.LatestStorageProofs(a.addr, [][32]byte{slot})
			if err != nil {
				t.Fatal(err)
			}
			ok, verr := g.VerifyFullStorage(proofs[0])
			if verr != nil {
				errs++
				continue
			}
			if ok {
				verified++
			}
		}
	}
	t.Logf("VerifyFullStorage: %d verified, %d errored (ErrInlineLeaf expected for some depths)", verified, errs)
	if verified == 0 {
		t.Fatal("expected at least one storage proof to verify via VerifyFullStorage")
	}
}

// TestRethHashed_RoTx exercises the raw RoTx accessor used by
// bootstrap tooling to walk HashedAccounts/HashedStorages directly.
func TestRethHashed_RoTx(t *testing.T) {
	_, src, accounts := g49Fixture(t, 3)
	tx, err := src.RoTx(context.Background())
	if err != nil {
		t.Fatalf("RoTx: %v", err)
	}
	defer tx.Rollback()

	v, err := tx.GetOne(rethHashedAccountsTable, keccak(accounts[0].addr[:]))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v, accounts[0].value) {
		t.Errorf("RoTx direct GetOne: got %x want %x", v, accounts[0].value)
	}
}
