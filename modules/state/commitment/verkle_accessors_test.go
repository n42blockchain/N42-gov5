// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers VerkleCommitment.InternalRoot and FlushTo, left untested by the
// existing basic round-trip test: InternalRoot must expose the exact
// gverkle.VerkleNode the commitment mutates in place, and FlushTo must write
// through to an arbitrary target store (used for per-block persistence into an
// MDBX write transaction distinct from the read-time store).

package commitment

import (
	"testing"

	gverkle "github.com/ethereum/go-verkle"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	libverkle "github.com/n42blockchain/N42/lib/verkle"
)

func TestVerkleCommitmentInternalRoot(t *testing.T) {
	root := gverkle.New()
	store := libverkle.NewMemStore()
	vc := NewVerkleCommitment(root, store)

	if vc.InternalRoot() != root {
		t.Fatal("InternalRoot() must return the exact node passed to NewVerkleCommitment")
	}

	addr := types.HexToAddress("0xabcdef0000000000000000000000000000abcd")
	acct := &account.StateAccount{Nonce: 1, Initialised: true}
	acct.Balance.SetUint64(10)
	if err := vc.UpdateAccount(addr, acct); err != nil {
		t.Fatal(err)
	}
	// InternalRoot must still be the same node (mutated in place), not a copy.
	if vc.InternalRoot() != root {
		t.Fatal("InternalRoot() must remain the same instance after mutation")
	}
}

func TestVerkleCommitmentFlushTo(t *testing.T) {
	root := gverkle.New()
	store := libverkle.NewMemStore()
	vc := NewVerkleCommitment(root, store)

	addr := types.HexToAddress("0x1111111111111111111111111111111111aaaa")
	acct := &account.StateAccount{Nonce: 2, Initialised: true}
	acct.Balance.SetUint64(55)
	if err := vc.UpdateAccount(addr, acct); err != nil {
		t.Fatal(err)
	}
	vc.Root()

	target := libverkle.NewMemStore()
	nodes, bytes, err := vc.FlushTo(target)
	if err != nil {
		t.Fatalf("FlushTo: %v", err)
	}
	if nodes == 0 || bytes == 0 {
		t.Fatalf("expected FlushTo to persist nodes/bytes, got nodes=%d bytes=%d", nodes, bytes)
	}
	// Flushing to the target must not have touched the original store.
	if store.Len() != 0 {
		t.Fatalf("FlushTo must write only to the given target, original store has %d entries", store.Len())
	}
	if target.Len() == 0 {
		t.Fatal("target store should contain the flushed nodes")
	}
}
