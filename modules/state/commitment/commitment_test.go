// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package commitment

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
)

func newTestCommitment() *JMTCommitment {
	s := jmt.NewMemStore()
	tree := jmt.New(s)
	return NewJMTCommitment(tree)
}

func makeAddr(i byte) types.Address {
	var addr types.Address
	addr[19] = i
	return addr
}

func makeSlot(i byte) types.Hash {
	var h types.Hash
	h[31] = i
	return h
}

// --- AccountKeyHash / StorageKeyHash ---

func TestKeyHashDeterministic(t *testing.T) {
	addr := makeAddr(1)
	h1 := AccountKeyHash(addr)
	h2 := AccountKeyHash(addr)
	if h1 != h2 {
		t.Fatal("account key hash should be deterministic")
	}

	slot := makeSlot(1)
	s1 := StorageKeyHash(addr, slot)
	s2 := StorageKeyHash(addr, slot)
	if s1 != s2 {
		t.Fatal("storage key hash should be deterministic")
	}
}

func TestKeyHashNoCollision(t *testing.T) {
	addr1 := makeAddr(1)
	addr2 := makeAddr(2)
	if AccountKeyHash(addr1) == AccountKeyHash(addr2) {
		t.Fatal("different addresses should have different hashes")
	}

	slot := makeSlot(1)
	if StorageKeyHash(addr1, slot) == StorageKeyHash(addr2, slot) {
		t.Fatal("same slot different addr should differ")
	}
	if AccountKeyHash(addr1) == StorageKeyHash(addr1, slot) {
		t.Fatal("account key and storage key should differ")
	}
}

// --- Account encoding ---

func TestAccountEncodingRoundTrip(t *testing.T) {
	original := &account.StateAccount{
		Initialised: true,
		Nonce:       42,
		Incarnation: 3,
	}
	original.Balance.SetUint64(1000000)
	original.CodeHash = types.BytesHash([]byte("code"))
	original.Root = types.BytesHash([]byte("root"))

	encoded := EncodeAccountValue(original)
	if len(encoded) != accountEncodingSize {
		t.Fatalf("expected %d bytes, got %d", accountEncodingSize, len(encoded))
	}

	var decoded account.StateAccount
	if err := DecodeAccountValue(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Initialised != original.Initialised {
		t.Fatal("Initialised mismatch")
	}
	if decoded.Nonce != original.Nonce {
		t.Fatal("Nonce mismatch")
	}
	if decoded.Balance.Cmp(&original.Balance) != 0 {
		t.Fatalf("Balance mismatch: %v != %v", decoded.Balance, original.Balance)
	}
	if decoded.Incarnation != original.Incarnation {
		t.Fatal("Incarnation mismatch")
	}
	if decoded.CodeHash != original.CodeHash {
		t.Fatal("CodeHash mismatch")
	}
	if decoded.Root != original.Root {
		t.Fatal("Root mismatch")
	}
}

func TestAccountEncodingZeroValues(t *testing.T) {
	original := &account.StateAccount{}
	encoded := EncodeAccountValue(original)
	var decoded account.StateAccount
	if err := DecodeAccountValue(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Initialised || decoded.Nonce != 0 || !decoded.Balance.IsZero() {
		t.Fatal("zero account should decode to zero values")
	}
}

// --- JMTCommitment ---

func TestCommitmentUpdateAccount(t *testing.T) {
	c := newTestCommitment()
	addr := makeAddr(1)
	acct := &account.StateAccount{
		Initialised: true,
		Nonce:       1,
	}
	acct.Balance.SetUint64(100)

	if err := c.UpdateAccount(addr, acct); err != nil {
		t.Fatal(err)
	}

	root := c.Root()
	var empty types.Hash
	if root == empty {
		t.Fatal("root should not be empty after update")
	}
}

func TestCommitmentDeleteAccount(t *testing.T) {
	c := newTestCommitment()
	addr := makeAddr(1)
	acct := &account.StateAccount{
		Initialised: true,
		Nonce:       1,
	}
	acct.Balance.SetUint64(100)

	c.UpdateAccount(addr, acct)
	if err := c.DeleteAccount(addr); err != nil {
		t.Fatal(err)
	}

	var empty types.Hash
	if c.Root() != empty {
		t.Fatal("root should be empty after deleting only account")
	}
}

func TestCommitmentEmptyAccountPruned(t *testing.T) {
	c := newTestCommitment()
	addr := makeAddr(1)

	// First insert a real account.
	acct := &account.StateAccount{Initialised: true, Nonce: 1}
	acct.Balance.SetUint64(100)
	c.UpdateAccount(addr, acct)

	// Update to empty — should be pruned.
	empty := &account.StateAccount{}
	c.UpdateAccount(addr, empty)

	var emptyHash types.Hash
	if c.Root() != emptyHash {
		t.Fatal("empty account should be pruned")
	}
}

func TestCommitmentUpdateStorage(t *testing.T) {
	c := newTestCommitment()
	addr := makeAddr(1)
	slot := makeSlot(1)
	val := uint256.NewInt(42)

	if err := c.UpdateStorage(addr, slot, val); err != nil {
		t.Fatal(err)
	}

	var empty types.Hash
	if c.Root() == empty {
		t.Fatal("root should not be empty after storage update")
	}
}

func TestCommitmentDeleteStorage(t *testing.T) {
	c := newTestCommitment()
	addr := makeAddr(1)
	slot := makeSlot(1)
	val := uint256.NewInt(42)

	c.UpdateStorage(addr, slot, val)
	zero := uint256.NewInt(0)
	if err := c.UpdateStorage(addr, slot, zero); err != nil {
		t.Fatal(err)
	}

	var empty types.Hash
	if c.Root() != empty {
		t.Fatal("root should be empty after deleting only storage")
	}
}

func TestCommitmentMultipleAccountsRoot(t *testing.T) {
	c := newTestCommitment()
	const n = 50
	for i := byte(0); i < n; i++ {
		acct := &account.StateAccount{
			Initialised: true,
			Nonce:       uint64(i),
		}
		acct.Balance.SetUint64(uint64(i) * 1000)
		c.UpdateAccount(makeAddr(i), acct)
	}

	root1 := c.Root()

	// Build same tree in different order.
	c2 := newTestCommitment()
	for i := byte(n - 1); ; i-- {
		acct := &account.StateAccount{
			Initialised: true,
			Nonce:       uint64(i),
		}
		acct.Balance.SetUint64(uint64(i) * 1000)
		c2.UpdateAccount(makeAddr(i), acct)
		if i == 0 {
			break
		}
	}

	root2 := c2.Root()
	if root1 != root2 {
		t.Fatal("same accounts in different order should produce same root")
	}
}

func TestCommitmentFlush(t *testing.T) {
	s := jmt.NewMemStore()
	tree := jmt.New(s)
	c := NewJMTCommitment(tree)

	for i := byte(0); i < 10; i++ {
		acct := &account.StateAccount{Initialised: true, Nonce: uint64(i)}
		acct.Balance.SetUint64(uint64(i))
		c.UpdateAccount(makeAddr(i), acct)
	}

	if c.DirtyNodeCount() == 0 {
		t.Fatal("should have dirty nodes before flush")
	}

	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	if c.DirtyNodeCount() != 0 {
		t.Fatal("should have no dirty nodes after flush")
	}

	// Verify store has nodes.
	if s.Len() == 0 {
		t.Fatal("store should have nodes after flush")
	}
}

func TestCommitmentAccountProof(t *testing.T) {
	c := newTestCommitment()
	addr := makeAddr(1)
	acct := &account.StateAccount{Initialised: true, Nonce: 1}
	acct.Balance.SetUint64(100)
	c.UpdateAccount(addr, acct)

	proof, err := c.GetAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Value == nil {
		t.Fatal("expected inclusion proof")
	}

	// Verify the proof.
	root := c.tree.Root()
	val, err := jmt.VerifyProof(root, proof, jmt.DefaultHasher())
	if err != nil {
		t.Fatal(err)
	}

	// Decode and verify the value.
	var decoded account.StateAccount
	if err := DecodeAccountValue(val, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Nonce != 1 || decoded.Balance.Uint64() != 100 {
		t.Fatal("proof value mismatch")
	}
}

func TestCommitmentStorageProof(t *testing.T) {
	c := newTestCommitment()
	addr := makeAddr(1)
	slot := makeSlot(42)
	val := uint256.NewInt(999)
	c.UpdateStorage(addr, slot, val)

	proof, err := c.GetStorageProof(addr, slot)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Value == nil {
		t.Fatal("expected inclusion proof")
	}

	root := c.tree.Root()
	got, err := jmt.VerifyProof(root, proof, jmt.DefaultHasher())
	if err != nil {
		t.Fatal(err)
	}

	var decoded uint256.Int
	decoded.SetBytes(got)
	if decoded.Uint64() != 999 {
		t.Fatalf("storage proof value: got %d, want 999", decoded.Uint64())
	}
}

func TestCommitmentMixedAccountAndStorage(t *testing.T) {
	c := newTestCommitment()

	// Add accounts and storage.
	for i := byte(0); i < 10; i++ {
		acct := &account.StateAccount{Initialised: true, Nonce: uint64(i)}
		acct.Balance.SetUint64(uint64(i) * 100)
		c.UpdateAccount(makeAddr(i), acct)

		for j := byte(0); j < 5; j++ {
			val := uint256.NewInt(uint64(i)*100 + uint64(j) + 1)
			c.UpdateStorage(makeAddr(i), makeSlot(j), val)
		}
	}

	root := c.Root()
	var empty types.Hash
	if root == empty {
		t.Fatal("root should not be empty")
	}

	// Verify all proofs.
	for i := byte(0); i < 10; i++ {
		proof, err := c.GetAccountProof(makeAddr(i))
		if err != nil {
			t.Fatalf("account proof %d: %v", i, err)
		}
		if proof.Value == nil {
			t.Fatalf("account %d: expected inclusion", i)
		}

		for j := byte(0); j < 5; j++ {
			sproof, err := c.GetStorageProof(makeAddr(i), makeSlot(j))
			if err != nil {
				t.Fatalf("storage proof %d/%d: %v", i, j, err)
			}
			if sproof.Value == nil {
				t.Fatalf("storage %d/%d: expected inclusion", i, j)
			}
		}
	}
}
