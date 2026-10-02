// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers the staged catch-up entry points in merkle_stage.go that
// merkle_stage_async_test.go leaves untouched: BuildRetainListFromChangesets
// reading REAL AccountChangeSet/StorageChangeSet rows (not a hand-built
// RetainList), MerkleStageIncremental end-to-end, the *RL/WithProof/Collect
// variants, and the proof-only Extract* twins. The reference root in every
// case is the synchronous incremental ComputeRoot path already proven correct
// by TestTrieRootIncrementalMatchesFull.

package commitment

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

// msBootstrap builds a fresh memdb with the mpCases[3] base state committed
// via a full (non-incremental) ComputeRoot, giving HashedAccounts/HashedStorage
// + TrieOfAccounts/TrieOfStorage all at the base root.
func msBootstrap(t *testing.T) (kv.RwTx, map[types.Address]*account.StateAccount, []types.Address) {
	t.Helper()
	base := mpCases[3]
	accts, stor, addrs := buildTestData(base)

	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Rollback)
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	if _, err := trc.ComputeRoot(accts, stor); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	_ = stor
	return tx, accts, addrs
}

// msDelta builds a delta touching: balance-only update, a storage slot
// change, a brand-new account with storage, and an account deletion — the
// same shape merkle_stage_async_test.go uses, so results are cross-checkable.
func msDelta(accts map[types.Address]*account.StateAccount, addrs []types.Address) (
	map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int,
) {
	dA := map[types.Address]*account.StateAccount{}
	dS := map[types.Address]map[types.Hash]*uint256.Int{}

	na := *accts[addrs[0]]
	na.Balance.SetUint64(31337)
	dA[addrs[0]] = &na

	var s types.Hash
	s[31] = 1
	nb := *accts[addrs[1]]
	dA[addrs[1]] = &nb
	dS[addrs[1]] = map[types.Hash]*uint256.Int{s: uint256.NewInt(987654)}

	var anew types.Address
	anew[0], anew[19] = 9, 42
	acctNew := &account.StateAccount{Initialised: true, Nonce: 3}
	acctNew.Balance.SetUint64(777)
	dA[anew] = acctNew
	dS[anew] = map[types.Hash]*uint256.Int{s: uint256.NewInt(555)}

	dA[addrs[4]] = nil // delete an account that has storage
	return dA, dS
}

// writeChangesets records a real AccountChangeSet/StorageChangeSet entry for
// every touched key at block number blockN. The old-value payload is
// irrelevant to BuildRetainListFromChangesets (it only reads keys), so empty
// placeholders are used — exactly the minimum the decoders accept.
func writeChangesets(t *testing.T, tx kv.RwTx, blockN uint64, dA map[types.Address]*account.StateAccount, dS map[types.Address]map[types.Hash]*uint256.Int) {
	t.Helper()
	acs := changeset.NewAccountChangeSet()
	for addr := range dA {
		acs.Changes = append(acs.Changes, changeset.Change{Key: addr[:]})
	}
	if err := changeset.EncodeAccounts(blockN, acs, func(k, v []byte) error {
		return tx.Put(modules.AccountChangeSet, k, v)
	}); err != nil {
		t.Fatalf("EncodeAccounts: %v", err)
	}

	scs := changeset.NewStorageChangeSet()
	for addr, slots := range dS {
		for slot := range slots {
			key := make([]byte, 0, len(addr)+len(slot))
			key = append(key, addr[:]...)
			key = append(key, slot[:]...)
			scs.Changes = append(scs.Changes, changeset.Change{Key: key})
		}
	}
	if err := changeset.EncodeStorage(blockN, scs, func(k, v []byte) error {
		return tx.Put(modules.StorageChangeSet, k, v)
	}); err != nil {
		t.Fatalf("EncodeStorage: %v", err)
	}
}

// applyWriteOnly writes the delta's HashedAccounts/HashedStorage (post-state)
// WITHOUT touching TrieOf* — the "writeOnly execution" contract merkle_stage.go
// documents as the caller's responsibility before MerkleStageIncremental runs.
func applyWriteOnly(t *testing.T, tx kv.RwTx, dA map[types.Address]*account.StateAccount, dS map[types.Address]map[types.Hash]*uint256.Int) {
	t.Helper()
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(true)
	trc.SetWriteOnly(true)
	if _, err := trc.ComputeRoot(dA, dS); err != nil {
		t.Fatalf("writeOnly exec: %v", err)
	}
}

// referenceRoot computes the trusted incremental root for the same delta
// against a parallel bootstrap, via the already-proven synchronous path.
func referenceRoot(t *testing.T, accts map[types.Address]*account.StateAccount, addrs []types.Address) types.Hash {
	t.Helper()
	base := mpCases[3]
	a2, s2, _ := buildTestData(base)
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Rollback)
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	if _, err := trc.ComputeRoot(a2, s2); err != nil {
		t.Fatal(err)
	}
	dA, dS := msDelta(a2, addrs)
	trc.SetIncremental(true)
	root, err := trc.ComputeRoot(dA, dS)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBuildRetainListFromChangesetsAndMerkleStageIncremental(t *testing.T) {
	tx, accts, addrs := msBootstrap(t)
	dA, dS := msDelta(accts, addrs)
	applyWriteOnly(t, tx, dA, dS)
	writeChangesets(t, tx, 1, dA, dS)

	rl, nAcc, nSto, err := BuildRetainListFromChangesets(tx, 1, 1)
	if err != nil {
		t.Fatalf("BuildRetainListFromChangesets: %v", err)
	}
	if nAcc != 4 { // addrs[0], addrs[1], anew, addrs[4] (deleted)
		t.Fatalf("nAcc = %d, want 4", nAcc)
	}
	if nSto != 2 { // addrs[1]'s slot, anew's slot
		t.Fatalf("nSto = %d, want 2", nSto)
	}
	if rl == nil {
		t.Fatal("expected a non-nil RetainList")
	}

	root, err := MerkleStageIncremental(tx, 1, 1)
	if err != nil {
		t.Fatalf("MerkleStageIncremental: %v", err)
	}
	want := referenceRoot(t, accts, addrs)
	if root != want {
		t.Fatalf("MerkleStageIncremental root mismatch:\n got  %x\n want %x", root, want)
	}
}

func TestMerkleStageIncrementalRLVariants(t *testing.T) {
	tx, accts, addrs := msBootstrap(t)
	dA, dS := msDelta(accts, addrs)
	applyWriteOnly(t, tx, dA, dS)
	writeChangesets(t, tx, 1, dA, dS)
	want := referenceRoot(t, accts, addrs)

	rl, _, _, err := BuildRetainListFromChangesets(tx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := MerkleStageIncrementalRL(tx, rl)
	if err != nil {
		t.Fatalf("MerkleStageIncrementalRL: %v", err)
	}
	if root != want {
		t.Fatalf("MerkleStageIncrementalRL root mismatch:\n got  %x\n want %x", root, want)
	}
}

func TestMerkleStageIncrementalWithProofRL(t *testing.T) {
	tx, accts, addrs := msBootstrap(t)
	dA, dS := msDelta(accts, addrs)
	applyWriteOnly(t, tx, dA, dS)
	writeChangesets(t, tx, 1, dA, dS)
	want := referenceRoot(t, accts, addrs)

	rl, _, _, err := BuildRetainListFromChangesets(tx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	root, proof, err := MerkleStageIncrementalWithProofRL(tx, rl)
	if err != nil {
		t.Fatalf("MerkleStageIncrementalWithProofRL: %v", err)
	}
	if root != want {
		t.Fatalf("root mismatch:\n got  %x\n want %x", root, want)
	}
	if len(proof) == 0 {
		t.Fatal("expected a non-empty captured proof for a non-trivial touched-key set")
	}
}

func TestMerkleStageIncrementalWithProof(t *testing.T) {
	tx, accts, addrs := msBootstrap(t)
	dA, dS := msDelta(accts, addrs)
	applyWriteOnly(t, tx, dA, dS)
	writeChangesets(t, tx, 1, dA, dS)
	want := referenceRoot(t, accts, addrs)

	root, proof, err := MerkleStageIncrementalWithProof(tx, 1, 1)
	if err != nil {
		t.Fatalf("MerkleStageIncrementalWithProof: %v", err)
	}
	if root != want {
		t.Fatalf("root mismatch:\n got  %x\n want %x", root, want)
	}
	if len(proof) == 0 {
		t.Fatal("expected a non-empty captured proof")
	}
}

func TestMerkleStageIncrementalCollect(t *testing.T) {
	tx, accts, addrs := msBootstrap(t)
	dA, dS := msDelta(accts, addrs)
	applyWriteOnly(t, tx, dA, dS)
	writeChangesets(t, tx, 1, dA, dS)
	want := referenceRoot(t, accts, addrs)

	root, upd, err := MerkleStageIncrementalCollect(tx, 1, 1)
	if err != nil {
		t.Fatalf("MerkleStageIncrementalCollect: %v", err)
	}
	if root != want {
		t.Fatalf("root mismatch:\n got  %x\n want %x", root, want)
	}
	if len(upd.Acc) == 0 && len(upd.Stor) == 0 {
		t.Fatal("expected some collected TrieOf* node updates")
	}
	if err := upd.Apply(tx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func TestExtractBlockMultiproofAndExtractMultiproof(t *testing.T) {
	tx, accts, addrs := msBootstrap(t)
	dA, dS := msDelta(accts, addrs)
	applyWriteOnly(t, tx, dA, dS)
	writeChangesets(t, tx, 1, dA, dS)
	want := referenceRoot(t, accts, addrs)

	root, proof, err := ExtractBlockMultiproof(tx, 1, 1)
	if err != nil {
		t.Fatalf("ExtractBlockMultiproof: %v", err)
	}
	if root != want {
		t.Fatalf("ExtractBlockMultiproof root mismatch:\n got  %x\n want %x", root, want)
	}
	if len(proof) == 0 {
		t.Fatal("expected a non-empty multiproof")
	}

	// ExtractMultiproof is a no-op on TrieOf* (read-only loader with no-op
	// collectors): running it again must reconstruct to the same root and must
	// not have changed TrieOfAccounts/TrieOfStorage (they were never flushed by
	// this call, only by MerkleStageIncremental* in the earlier calls — so a
	// second extraction over the same RetainList lands on the same root
	// idempotently).
	rl, _, _, err := BuildRetainListFromChangesets(tx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	root2, proof2, err := ExtractMultiproof(tx, rl)
	if err != nil {
		t.Fatalf("ExtractMultiproof: %v", err)
	}
	if root2 != want {
		t.Fatalf("ExtractMultiproof root mismatch:\n got  %x\n want %x", root2, want)
	}
	if len(proof2) == 0 {
		t.Fatal("expected a non-empty multiproof from ExtractMultiproof")
	}
}
