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

package witness

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// TestIntegration_WitnessGenerateVerifyRead is an end-to-end test that:
//  1. Creates a JMT tree with 5 accounts and storage slots for 2 of them
//  2. Uses TracingReader to record access to 3 accounts + 2 storage slots
//  3. Generates a witness via Generator
//  4. Verifies the witness against the state root
//  5. Creates a WitnessStateReader and reads back account data by real address
func TestIntegration_WitnessGenerateVerifyRead(t *testing.T) {
	// --- Step 1: Set up JMT with 5 accounts ---

	store := jmt.NewMemStore()
	tree := jmt.New(store)
	jmtCommit := commitment.NewJMTCommitment(tree)

	type testAddr struct {
		addr types.Address
		acct *account.StateAccount
	}

	addresses := []testAddr{
		{addr: types.HexToAddress("0x1000000000000000000000000000000000000001")},
		{addr: types.HexToAddress("0x2000000000000000000000000000000000000002")},
		{addr: types.HexToAddress("0x3000000000000000000000000000000000000003")},
		{addr: types.HexToAddress("0x4000000000000000000000000000000000000004")},
		{addr: types.HexToAddress("0x5000000000000000000000000000000000000005")},
	}

	// Initialize accounts with different balances and nonces.
	for i := range addresses {
		acct := &account.StateAccount{
			Nonce:       uint64(i + 1),
			Initialised: true,
			Incarnation: 1,
		}
		acct.Balance.SetUint64(uint64((i + 1) * 10000))
		addresses[i].acct = acct

		if err := jmtCommit.UpdateAccount(addresses[i].addr, acct); err != nil {
			t.Fatalf("failed to update account %d: %v", i, err)
		}
	}

	// Add storage slots for accounts 0 and 1.
	storageSlots := []struct {
		addr  types.Address
		slot  types.Hash
		value *uint256.Int
	}{
		{
			addr:  addresses[0].addr,
			slot:  types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
			value: uint256.NewInt(42),
		},
		{
			addr:  addresses[0].addr,
			slot:  types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"),
			value: uint256.NewInt(99),
		},
		{
			addr:  addresses[1].addr,
			slot:  types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
			value: uint256.NewInt(7777),
		},
	}

	for _, s := range storageSlots {
		if err := jmtCommit.UpdateStorage(s.addr, s.slot, s.value); err != nil {
			t.Fatalf("failed to update storage: %v", err)
		}
	}

	if err := tree.Flush(); err != nil {
		t.Fatal(err)
	}

	parentRoot := jmtCommit.Root()
	if parentRoot == (types.Hash{}) {
		t.Fatal("expected non-empty root after inserting accounts")
	}

	// --- Step 2: Use TracingReader to record access to 3 accounts ---
	// We create a real state reader backed by JMT and wrap it in TracingReader.

	jmtReader := &jmtStateReader{commitment: jmtCommit}
	tracer := NewTracingReader(jmtReader)

	// Access 3 of the 5 accounts (indices 0, 2, 4).
	accessedIndices := []int{0, 2, 4}
	for _, idx := range accessedIndices {
		acct, err := tracer.ReadAccountData(addresses[idx].addr)
		if err != nil {
			t.Fatalf("ReadAccountData(%d) failed: %v", idx, err)
		}
		if acct == nil {
			t.Fatalf("expected non-nil account at index %d", idx)
		}
		if acct.Nonce != uint64(idx+1) {
			t.Fatalf("expected nonce %d, got %d", idx+1, acct.Nonce)
		}
	}

	// Access storage for account 0.
	slot1 := storageSlots[0].slot
	val, err := tracer.ReadAccountStorage(addresses[0].addr, 1, &slot1)
	if err != nil {
		t.Fatal(err)
	}
	if len(val) == 0 {
		t.Fatal("expected non-empty storage value")
	}

	// Verify tracer recorded the correct access set.
	accessedAccounts := tracer.AccessedAccounts()
	if len(accessedAccounts) != 3 {
		t.Fatalf("expected 3 accessed accounts, got %d", len(accessedAccounts))
	}

	accessedStorage := tracer.AccessedStorage()
	if len(accessedStorage) != 1 {
		t.Fatalf("expected 1 address with storage access, got %d", len(accessedStorage))
	}

	// --- Step 3: Generate witness ---

	gen, err := NewGenerator(jmtCommit)
	if err != nil {
		t.Fatalf("NewGenerator failed: %v", err)
	}
	w, err := gen.Generate(parentRoot, tracer, nil /* no codes */)
	if err != nil {
		t.Fatalf("witness generation failed: %v", err)
	}

	if w.ParentRoot != parentRoot {
		t.Fatal("witness parent root mismatch")
	}
	if len(w.AccountProofs) != 3 {
		t.Fatalf("expected 3 account proofs, got %d", len(w.AccountProofs))
	}
	if len(w.StorageProofs) != 1 {
		t.Fatalf("expected 1 storage proof, got %d", len(w.StorageProofs))
	}

	// Verify that OriginalKey is set on all proofs.
	for i, ap := range w.AccountProofs {
		if len(ap.OriginalKey) != types.AddressLength {
			t.Fatalf("account proof %d: OriginalKey length %d, expected %d", i, len(ap.OriginalKey), types.AddressLength)
		}
	}
	for i, sp := range w.StorageProofs {
		expectedLen := types.AddressLength + types.HashLength
		if len(sp.OriginalKey) != expectedLen {
			t.Fatalf("storage proof %d: OriginalKey length %d, expected %d", i, len(sp.OriginalKey), expectedLen)
		}
	}

	// --- Step 4: Verify the witness ---

	if err := VerifyWitness(parentRoot, w); err != nil {
		t.Fatalf("witness verification failed: %v", err)
	}

	// Also test VerifyBlockStateless.
	verifiedRoot, err := VerifyBlockStateless(parentRoot, w, nil)
	if err != nil {
		t.Fatalf("VerifyBlockStateless failed: %v", err)
	}
	if verifiedRoot != parentRoot {
		t.Fatal("VerifyBlockStateless returned unexpected root")
	}

	// --- Step 5: Create WitnessStateReader and read back data ---

	reader, err := NewWitnessStateReader(w)
	if err != nil {
		t.Fatalf("NewWitnessStateReader failed: %v", err)
	}

	// Read back each accessed account by its real address.
	for _, idx := range accessedIndices {
		addr := addresses[idx].addr
		readAcct, err := reader.ReadAccountData(addr)
		if err != nil {
			t.Fatalf("ReadAccountData failed for addr %s: %v", addr.Hex(), err)
		}
		if readAcct == nil {
			t.Fatalf("expected non-nil account for addr %s", addr.Hex())
		}
		if !readAcct.Initialised {
			t.Fatal("expected account to be initialised")
		}
		if readAcct.Balance.IsZero() {
			t.Fatal("expected non-zero balance")
		}
		if readAcct.Nonce != uint64(idx+1) {
			t.Fatalf("expected nonce %d, got %d", idx+1, readAcct.Nonce)
		}
	}

	// Verify reading an address not in the witness returns nil (not-in-witness).
	unknownAddr := types.HexToAddress("0xffffffffffffffffffffffffffffffffffffffff")
	unknownAcct, err := reader.ReadAccountData(unknownAddr)
	if err != nil {
		t.Fatal(err)
	}
	if unknownAcct != nil {
		t.Fatal("expected nil for address not in witness")
	}

	// Read storage from witness by real address and slot.
	storageVal, err := reader.ReadAccountStorage(addresses[0].addr, 1, &slot1)
	if err != nil {
		t.Fatal(err)
	}
	if len(storageVal) == 0 {
		t.Fatal("expected non-empty storage value from witness reader")
	}
}

// TestIntegration_WitnessEncodeDecodeRoundTrip tests full encode/decode
// round-trip with a real witness containing multiple proofs.
func TestIntegration_WitnessEncodeDecodeRoundTrip(t *testing.T) {
	store := jmt.NewMemStore()
	tree := jmt.New(store)
	jmtCommit := commitment.NewJMTCommitment(tree)

	// Insert 3 accounts.
	addrs := make([]types.Address, 3)
	for i := byte(1); i <= 3; i++ {
		var addr types.Address
		addr[19] = i
		addrs[i-1] = addr
		acct := &account.StateAccount{
			Nonce:       uint64(i),
			Initialised: true,
		}
		acct.Balance.SetUint64(uint64(i) * 100)
		if err := jmtCommit.UpdateAccount(addr, acct); err != nil {
			t.Fatal(err)
		}
	}
	if err := tree.Flush(); err != nil {
		t.Fatal(err)
	}

	root := jmtCommit.Root()

	// Generate proofs for all 3.
	var proofs []KeyProof
	for _, addr := range addrs {
		proof, err := jmtCommit.GetAccountProof(addr)
		if err != nil {
			t.Fatal(err)
		}
		proofs = append(proofs, KeyProof{
			KeyHash:     proof.Key,
			OriginalKey: addr[:],
			Value:       proof.Value,
			Proof:       *proof,
		})
	}

	w := &BlockWitness{
		ParentRoot:    root,
		AccountProofs: proofs,
	}

	// Encode.
	data, err := w.Encode()
	if err != nil {
		t.Fatal(err)
	}

	// Decode.
	decoded, err := DecodeBlockWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	// Verify decoded witness.
	if decoded.ParentRoot != root {
		t.Fatal("parent root mismatch after round-trip")
	}
	if len(decoded.AccountProofs) != 3 {
		t.Fatalf("expected 3 account proofs after round-trip, got %d", len(decoded.AccountProofs))
	}

	// Verify OriginalKey survived round-trip.
	for i, ap := range decoded.AccountProofs {
		if len(ap.OriginalKey) != types.AddressLength {
			t.Fatalf("account proof %d: OriginalKey lost during round-trip", i)
		}
	}

	// The decoded witness should still verify.
	if err := VerifyWitness(root, decoded); err != nil {
		t.Fatalf("decoded witness verification failed: %v", err)
	}
}

// jmtStateReader implements state.StateReader by reading directly from
// the JMT commitment layer. Used for integration testing only.
type jmtStateReader struct {
	commitment *commitment.JMTCommitment
}

func (r *jmtStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	keyHash := commitment.AccountKeyHash(address)
	val, err := r.commitment.Tree().Get(keyHash)
	if err != nil {
		if err == jmt.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	acct := new(account.StateAccount)
	if err := commitment.DecodeAccountValue(val, acct); err != nil {
		return nil, err
	}
	return acct, nil
}

func (r *jmtStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	if key == nil {
		return nil, nil
	}
	keyHash := commitment.StorageKeyHash(address, *key)
	val, err := r.commitment.Tree().Get(keyHash)
	if err != nil {
		if err == jmt.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	return val, nil
}

func (r *jmtStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	return nil, nil
}

func (r *jmtStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	return 0, nil
}

func (r *jmtStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	return 0, nil
}

// TestNewGenerator_NilCommitment verifies NewGenerator returns error for nil JMTCommitment.
func TestNewGenerator_NilCommitment(t *testing.T) {
	gen, err := NewGenerator(nil)
	if err == nil {
		t.Fatal("expected error for nil JMTCommitment")
	}
	if gen != nil {
		t.Fatal("expected nil generator on error")
	}
}

// TestNewGenerator_ValidCommitment verifies NewGenerator succeeds with valid JMTCommitment.
func TestNewGenerator_ValidCommitment(t *testing.T) {
	store := jmt.NewMemStore()
	tree := jmt.New(store)
	jmtCommit := commitment.NewJMTCommitment(tree)
	gen, err := NewGenerator(jmtCommit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen == nil {
		t.Fatal("expected non-nil generator")
	}
}
