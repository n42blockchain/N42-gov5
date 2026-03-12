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

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// newTestCommitment creates a JMTCommitment backed by an in-memory store.
func newTestCommitment() *commitment.JMTCommitment {
	s := jmt.NewMemStore()
	tree := jmt.New(s)
	return commitment.NewJMTCommitment(tree)
}

// makeTestAccount creates a StateAccount with the given nonce and balance.
func makeTestAccount(nonce uint64, balance uint64) *account.StateAccount {
	acct := &account.StateAccount{
		Nonce:       nonce,
		Initialised: true,
	}
	acct.Balance.SetUint64(balance)
	return acct
}

// TestVerifyWitness_EmptyWitness tests verification of an empty witness
// against an empty root.
func TestVerifyWitness_EmptyWitness(t *testing.T) {
	w := &BlockWitness{
		ParentRoot: types.Hash{},
	}
	if err := VerifyWitness(types.Hash{}, w); err != nil {
		t.Fatalf("empty witness should verify against empty root: %v", err)
	}
}

// TestVerifyWitness_SingleAccount tests verification with one account proof.
func TestVerifyWitness_SingleAccount(t *testing.T) {
	jmtCommit := newTestCommitment()

	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	acct := makeTestAccount(1, 1000)

	if err := jmtCommit.UpdateAccount(addr, acct); err != nil {
		t.Fatal(err)
	}
	if err := jmtCommit.Tree().Flush(); err != nil {
		t.Fatal(err)
	}

	root := jmtCommit.Root()

	// Generate proof.
	proof, err := jmtCommit.GetAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}

	// Build witness with the proof.
	w := &BlockWitness{
		ParentRoot: root,
		AccountProofs: []KeyProof{
			{
				KeyHash:     proof.Key,
				OriginalKey: addr[:],
				Value:       proof.Value,
				Proof:       *proof,
			},
		},
	}

	// Verify should succeed.
	if err := VerifyWitness(root, w); err != nil {
		t.Fatalf("valid witness should verify: %v", err)
	}
}

// TestVerifyWitness_InvalidRoot tests verification with the wrong root.
func TestVerifyWitness_InvalidRoot(t *testing.T) {
	jmtCommit := newTestCommitment()

	addr := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	acct := makeTestAccount(5, 500)

	if err := jmtCommit.UpdateAccount(addr, acct); err != nil {
		t.Fatal(err)
	}
	if err := jmtCommit.Tree().Flush(); err != nil {
		t.Fatal(err)
	}

	root := jmtCommit.Root()

	proof, err := jmtCommit.GetAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}

	w := &BlockWitness{
		ParentRoot: root,
		AccountProofs: []KeyProof{
			{
				KeyHash:     proof.Key,
				OriginalKey: addr[:],
				Value:       proof.Value,
				Proof:       *proof,
			},
		},
	}

	// Verify with wrong root should fail.
	wrongRoot := types.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err := VerifyWitness(wrongRoot, w); err == nil {
		t.Fatal("verification with wrong root should fail")
	}
}

// TestVerifyWitness_MultipleAccounts tests verification with multiple account proofs.
func TestVerifyWitness_MultipleAccounts(t *testing.T) {
	jmtCommit := newTestCommitment()

	addrs := []types.Address{
		types.HexToAddress("0x1111111111111111111111111111111111111111"),
		types.HexToAddress("0x2222222222222222222222222222222222222222"),
		types.HexToAddress("0x3333333333333333333333333333333333333333"),
	}
	for i, addr := range addrs {
		acct := makeTestAccount(uint64(i+1), uint64((i+1)*1000))
		if err := jmtCommit.UpdateAccount(addr, acct); err != nil {
			t.Fatal(err)
		}
	}
	if err := jmtCommit.Tree().Flush(); err != nil {
		t.Fatal(err)
	}

	root := jmtCommit.Root()

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

	if err := VerifyWitness(root, w); err != nil {
		t.Fatalf("witness with multiple accounts should verify: %v", err)
	}
}

// TestVerifyWitness_TamperedValue tests that a tampered proof value is detected.
func TestVerifyWitness_TamperedValue(t *testing.T) {
	jmtCommit := newTestCommitment()

	addr := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	acct := makeTestAccount(10, 9999)

	if err := jmtCommit.UpdateAccount(addr, acct); err != nil {
		t.Fatal(err)
	}
	if err := jmtCommit.Tree().Flush(); err != nil {
		t.Fatal(err)
	}

	root := jmtCommit.Root()

	proof, err := jmtCommit.GetAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper with the value.
	tamperedValue := make([]byte, len(proof.Value))
	copy(tamperedValue, proof.Value)
	tamperedValue[0] ^= 0xFF // flip bits

	w := &BlockWitness{
		ParentRoot: root,
		AccountProofs: []KeyProof{
			{
				KeyHash:     proof.Key,
				OriginalKey: addr[:],
				Value:       tamperedValue,
				Proof:       *proof,
			},
		},
	}

	if err := VerifyWitness(root, w); err == nil {
		t.Fatal("tampered value should cause verification failure")
	}
}

// TestVerifyWitness_KeyHashMismatch tests that a mismatched KeyHash vs Proof.Key is detected.
func TestVerifyWitness_KeyHashMismatch(t *testing.T) {
	jmtCommit := newTestCommitment()

	addr := types.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")
	acct := makeTestAccount(1, 100)

	if err := jmtCommit.UpdateAccount(addr, acct); err != nil {
		t.Fatal(err)
	}
	if err := jmtCommit.Tree().Flush(); err != nil {
		t.Fatal(err)
	}

	root := jmtCommit.Root()

	proof, err := jmtCommit.GetAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}

	// Set a different KeyHash than Proof.Key.
	fakeKeyHash := proof.Key
	fakeKeyHash[0] ^= 0xFF

	w := &BlockWitness{
		ParentRoot: root,
		AccountProofs: []KeyProof{
			{
				KeyHash:     fakeKeyHash,
				OriginalKey: addr[:],
				Value:       proof.Value,
				Proof:       *proof,
			},
		},
	}

	if err := VerifyWitness(root, w); err == nil {
		t.Fatal("mismatched KeyHash vs Proof.Key should cause verification failure")
	}
}

// TestTracingReader_RecordsAccess tests that TracingReader correctly records
// all accessed accounts, storage slots, and codes.
func TestTracingReader_RecordsAccess(t *testing.T) {
	inner := &mockStateReader{}
	tracer := NewTracingReader(inner)

	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	// Read various state data.
	if _, err := tracer.ReadAccountData(addr1); err != nil {
		t.Fatal(err)
	}
	if _, err := tracer.ReadAccountData(addr2); err != nil {
		t.Fatal(err)
	}
	if _, err := tracer.ReadAccountStorage(addr1, 1, &slot); err != nil {
		t.Fatal(err)
	}
	if _, err := tracer.ReadAccountCode(addr2, 1, types.Hash{}); err != nil {
		t.Fatal(err)
	}

	// Check accessed accounts.
	accounts := tracer.AccessedAccounts()
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accessed accounts, got %d", len(accounts))
	}

	// Check accessed storage.
	storage := tracer.AccessedStorage()
	if len(storage) != 1 {
		t.Fatalf("expected 1 address with storage access, got %d", len(storage))
	}
	if slots, ok := storage[addr1]; !ok || len(slots) != 1 {
		t.Fatalf("expected 1 storage access for addr1, got %v", storage[addr1])
	}

	// Check accessed codes.
	codes := tracer.AccessedCodes()
	if len(codes) != 1 {
		t.Fatalf("expected 1 code access, got %d", len(codes))
	}
}

// TestTracingReader_DeduplicatesRepeatedAccess tests that repeated accesses
// to the same address are deduplicated.
func TestTracingReader_DeduplicatesRepeatedAccess(t *testing.T) {
	inner := &mockStateReader{}
	tracer := NewTracingReader(inner)

	addr := types.HexToAddress("0x4444444444444444444444444444444444444444")

	// Read the same account multiple times.
	for i := 0; i < 5; i++ {
		if _, err := tracer.ReadAccountData(addr); err != nil {
			t.Fatal(err)
		}
	}

	accounts := tracer.AccessedAccounts()
	if len(accounts) != 1 {
		t.Fatalf("expected 1 accessed account after dedup, got %d", len(accounts))
	}
}

// TestWitnessStateReader_ReadAccount tests reading account data from a
// witness state reader that was populated from JMT proofs with OriginalKey.
func TestWitnessStateReader_ReadAccount(t *testing.T) {
	jmtCommit := newTestCommitment()

	addr := types.HexToAddress("0x3333333333333333333333333333333333333333")
	acct := makeTestAccount(42, 12345)

	if err := jmtCommit.UpdateAccount(addr, acct); err != nil {
		t.Fatal(err)
	}
	if err := jmtCommit.Tree().Flush(); err != nil {
		t.Fatal(err)
	}

	root := jmtCommit.Root()
	proof, err := jmtCommit.GetAccountProof(addr)
	if err != nil {
		t.Fatal(err)
	}

	w := &BlockWitness{
		ParentRoot: root,
		AccountProofs: []KeyProof{
			{
				KeyHash:     proof.Key,
				OriginalKey: addr[:],
				Value:       proof.Value,
				Proof:       *proof,
			},
		},
	}

	reader, err := NewWitnessStateReader(w)
	if err != nil {
		t.Fatal(err)
	}

	// Now we can look up by the real address since OriginalKey is set.
	readAcct, err := reader.ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	if readAcct == nil {
		t.Fatal("expected non-nil account")
	}
	if readAcct.Nonce != 42 {
		t.Fatalf("expected nonce 42, got %d", readAcct.Nonce)
	}
	if readAcct.Balance.Uint64() != 12345 {
		t.Fatalf("expected balance 12345, got %d", readAcct.Balance.Uint64())
	}
}

// TestWitnessStateReader_ExclusionProof tests reading a non-existent account
// that has an exclusion proof in the witness.
func TestWitnessStateReader_ExclusionProof(t *testing.T) {
	jmtCommit := newTestCommitment()

	// Insert one account so tree is non-empty.
	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	if err := jmtCommit.UpdateAccount(addr1, makeTestAccount(1, 100)); err != nil {
		t.Fatal(err)
	}
	if err := jmtCommit.Tree().Flush(); err != nil {
		t.Fatal(err)
	}

	root := jmtCommit.Root()

	// Get proof for a non-existent account (exclusion proof).
	nonExistent := types.HexToAddress("0x9999999999999999999999999999999999999999")
	proof, err := jmtCommit.GetAccountProof(nonExistent)
	if err != nil {
		t.Fatal(err)
	}
	// Exclusion proof: Value should be nil.
	if proof.Value != nil {
		t.Fatal("expected nil value for exclusion proof")
	}

	w := &BlockWitness{
		ParentRoot: root,
		AccountProofs: []KeyProof{
			{
				KeyHash:     proof.Key,
				OriginalKey: nonExistent[:],
				Value:       nil,
				Proof:       *proof,
			},
		},
	}

	if err := VerifyWitness(root, w); err != nil {
		t.Fatalf("exclusion proof should verify: %v", err)
	}

	reader, err := NewWitnessStateReader(w)
	if err != nil {
		t.Fatal(err)
	}

	// Look up by the real address.
	readAcct, err := reader.ReadAccountData(nonExistent)
	if err != nil {
		t.Fatal(err)
	}
	// Account should be nil (proven absent).
	if readAcct != nil {
		t.Fatal("expected nil account for exclusion proof")
	}
}

// TestBlockWitness_Encoding tests JSON round-trip encoding of a BlockWitness.
func TestBlockWitness_Encoding(t *testing.T) {
	addr := types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	w := &BlockWitness{
		ParentRoot: types.HexToHash("0xabcdef0000000000000000000000000000000000000000000000000000000000"),
		AccountProofs: []KeyProof{
			{
				KeyHash: jmt.Hash{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
					17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
				OriginalKey: addr[:],
				Value:       []byte{4, 5, 6},
				Proof: jmt.Proof{
					Key: jmt.Hash{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
						17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
					Value: []byte{4, 5, 6},
					Path: []jmt.ProofEntry{
						{NodeData: []byte{7, 8, 9}, Nibble: 3},
					},
				},
			},
		},
		Codes: nil,
	}

	data, err := w.Encode()
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBlockWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.ParentRoot != w.ParentRoot {
		t.Fatal("parent root mismatch after round-trip")
	}
	if len(decoded.AccountProofs) != 1 {
		t.Fatal("account proofs count mismatch after round-trip")
	}
	if decoded.AccountProofs[0].KeyHash != w.AccountProofs[0].KeyHash {
		t.Fatal("key hash mismatch after round-trip")
	}
	if len(decoded.AccountProofs[0].OriginalKey) != types.AddressLength {
		t.Fatal("original key length mismatch after round-trip")
	}
	if len(decoded.AccountProofs[0].Proof.Path) != 1 {
		t.Fatal("proof path length mismatch after round-trip")
	}
}

// TestBlockWitness_EncodingEmpty tests encoding and decoding an empty witness.
func TestBlockWitness_EncodingEmpty(t *testing.T) {
	w := &BlockWitness{
		ParentRoot: types.Hash{},
	}

	data, err := w.Encode()
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeBlockWitness(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.ParentRoot != w.ParentRoot {
		t.Fatal("parent root mismatch for empty witness")
	}
	if len(decoded.AccountProofs) != 0 {
		t.Fatal("expected no account proofs in empty witness")
	}
}

// TestBlockWitness_DecodeSizeLimit tests that oversized witness data is rejected.
func TestBlockWitness_DecodeSizeLimit(t *testing.T) {
	oversized := make([]byte, MaxWitnessSize+1)
	_, err := DecodeBlockWitness(oversized)
	if err == nil {
		t.Fatal("expected error for oversized witness data")
	}
}

// TestExtractProofNodes tests the JMT ExtractProofNodes function.
func TestExtractProofNodes(t *testing.T) {
	store := jmt.NewMemStore()
	tree := jmt.New(store)

	key := jmt.HashKey([]byte("test-key"))
	if err := tree.Put(key, []byte("test-value")); err != nil {
		t.Fatal(err)
	}
	if err := tree.Flush(); err != nil {
		t.Fatal(err)
	}

	proof, err := tree.GetProof(key)
	if err != nil {
		t.Fatal(err)
	}

	nodes := jmt.ExtractProofNodes(proof, jmt.DefaultHasher())
	if len(nodes) == 0 {
		t.Fatal("expected at least one proof node")
	}
	if len(nodes) != len(proof.Path) {
		t.Fatalf("expected %d nodes, got %d", len(proof.Path), len(nodes))
	}

	// Verify each extracted node hash matches.
	hasher := jmt.DefaultHasher()
	for _, entry := range proof.Path {
		nodeHash := hasher.Hash(entry.NodeData)
		if _, ok := nodes[nodeHash]; !ok {
			t.Fatalf("extracted nodes should contain hash %x", nodeHash)
		}
	}
}

// TestVerifyBlockStateless_RootMismatch tests that VerifyBlockStateless
// rejects a witness whose parent root does not match.
func TestVerifyBlockStateless_RootMismatch(t *testing.T) {
	expectedRoot := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	witnessRoot := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	w := &BlockWitness{
		ParentRoot: witnessRoot,
	}

	_, err := VerifyBlockStateless(expectedRoot, w, nil)
	if err == nil {
		t.Fatal("expected error for root mismatch")
	}
}

// TestNewTracingReader_NilPanic tests that NewTracingReader panics with nil.
func TestNewTracingReader_NilPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil StateReader")
		}
	}()
	NewTracingReader(nil)
}

// mockStateReader is a no-op state reader for testing TracingReader.
type mockStateReader struct{}

func (m *mockStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	return nil, nil
}

func (m *mockStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	return nil, nil
}

func (m *mockStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	return nil, nil
}

func (m *mockStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	return 0, nil
}

func (m *mockStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	return 0, nil
}
