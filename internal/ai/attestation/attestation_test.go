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

package attestation

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// mockZKProvider implements ZKProofProvider for testing.
type mockZKProvider struct {
	proofs map[types.Hash][]byte
}

func (m *mockZKProvider) GetProof(requestID types.Hash) ([]byte, bool, error) {
	data, ok := m.proofs[requestID]
	return data, ok, nil
}

// mockTrainingVerification implements TrainingVerification for testing.
type mockTrainingVerification struct {
	records map[types.Hash]struct {
		modelHash types.Hash
		verified  bool
	}
}

func (m *mockTrainingVerification) GetRecord(recordID types.Hash) (types.Hash, bool, error) {
	rec, ok := m.records[recordID]
	if !ok {
		return types.Hash{}, false, errors.New("training record not found")
	}
	return rec.modelHash, rec.verified, nil
}

// testRequestID returns a deterministic request ID for testing.
func testRequestID() types.Hash {
	return types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
}

// testModelHash returns a deterministic model hash for testing.
func testModelHash() types.Hash {
	return types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
}

// newTestService creates an AttestationService with a mock ZK provider seeded with the given request.
func newTestService(requestID types.Hash, ttl time.Duration) *AttestationService {
	zk := &mockZKProvider{proofs: map[types.Hash][]byte{
		requestID: {0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
	}}
	return NewAttestationService(zk, nil, ttl, 0)
}

// createTestAttestation is a helper that creates a standard attestation for testing.
func createTestAttestation(t *testing.T, svc *AttestationService, requestID types.Hash) *InferenceAttestation {
	t.Helper()
	modelHash := testModelHash()
	inputHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	outputHash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	operator := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	att, err := svc.CreateAttestation(requestID, modelHash, inputHash, outputHash, types.Hash{}, SafetyStandard, operator, "did:n42:0xaaaa")
	if err != nil {
		t.Fatalf("CreateAttestation failed: %v", err)
	}
	return att
}

func TestAttestationService_CreateAndSign(t *testing.T) {
	requestID := testRequestID()
	svc := newTestService(requestID, time.Hour)

	att := createTestAttestation(t, svc, requestID)

	if att.ID == (types.Hash{}) {
		t.Fatal("expected non-zero attestation ID")
	}
	if att.RequestID != requestID {
		t.Errorf("RequestID = %s, want %s", att.RequestID.Hex(), requestID.Hex())
	}
	if att.Status != AttestPending {
		t.Errorf("Status = %s, want pending", att.Status)
	}
	if att.ZKProofHash == (types.Hash{}) {
		t.Error("expected non-zero ZKProofHash")
	}

	// Sign the attestation.
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	signed, err := svc.SignAttestation(att.ID, key)
	if err != nil {
		t.Fatalf("SignAttestation failed: %v", err)
	}
	if signed.Attestation.Status != AttestSigned {
		t.Errorf("signed Status = %s, want signed", signed.Attestation.Status)
	}
	if len(signed.Signature) == 0 {
		t.Error("expected non-empty signature")
	}
	if len(signed.SignerPubKey) == 0 {
		t.Error("expected non-empty signer public key")
	}

	// Verify signature.
	valid, err := svc.VerifySignature(signed)
	if err != nil {
		t.Fatalf("VerifySignature failed: %v", err)
	}
	if !valid {
		t.Error("expected valid signature")
	}
}

func TestAttestationService_VerifySignature(t *testing.T) {
	requestID := testRequestID()
	svc := newTestService(requestID, time.Hour)
	att := createTestAttestation(t, svc, requestID)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	signed, err := svc.SignAttestation(att.ID, key)
	if err != nil {
		t.Fatalf("SignAttestation failed: %v", err)
	}

	// Valid signature should verify.
	valid, err := svc.VerifySignature(signed)
	if err != nil {
		t.Fatalf("VerifySignature failed: %v", err)
	}
	if !valid {
		t.Error("expected valid signature")
	}

	// Tamper with the signature — flip a byte.
	tampered := &SignedAttestation{
		Attestation:  signed.Attestation,
		Signature:    make([]byte, len(signed.Signature)),
		SignerPubKey: make([]byte, len(signed.SignerPubKey)),
		SignedAt:     signed.SignedAt,
	}
	copy(tampered.Signature, signed.Signature)
	copy(tampered.SignerPubKey, signed.SignerPubKey)
	tampered.Signature[0] ^= 0xff

	valid, _ = svc.VerifySignature(tampered)
	if valid {
		t.Error("expected tampered signature to be invalid")
	}
}

func TestAttestationService_VerifyAttestation(t *testing.T) {
	requestID := testRequestID()
	svc := newTestService(requestID, time.Hour)
	att := createTestAttestation(t, svc, requestID)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	_, err = svc.SignAttestation(att.ID, key)
	if err != nil {
		t.Fatalf("SignAttestation failed: %v", err)
	}

	// Full verification: checks signature, expiry, revocation.
	valid, err := svc.VerifyAttestation(att.ID)
	if err != nil {
		t.Fatalf("VerifyAttestation failed: %v", err)
	}
	if !valid {
		t.Error("expected attestation to be valid")
	}

	// After verification, status should be AttestVerified.
	stored, found := svc.GetAttestation(att.ID)
	if !found {
		t.Fatal("attestation not found after verification")
	}
	if stored.Attestation.Status != AttestVerified {
		t.Errorf("Status = %s, want verified", stored.Attestation.Status)
	}
}

func TestAttestationService_Expiry(t *testing.T) {
	requestID := testRequestID()
	// Use a very short TTL.
	svc := newTestService(requestID, time.Millisecond)
	att := createTestAttestation(t, svc, requestID)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	// Wait for the attestation to expire.
	time.Sleep(5 * time.Millisecond)

	// Attempt to sign — should fail with ErrAttestationExpired.
	_, err = svc.SignAttestation(att.ID, key)
	if err == nil {
		t.Fatal("expected error for expired attestation")
	}
	if !errors.Is(err, ErrAttestationExpired) {
		t.Errorf("expected ErrAttestationExpired, got: %v", err)
	}
}

func TestAttestationService_Revocation(t *testing.T) {
	requestID := testRequestID()
	svc := newTestService(requestID, time.Hour)
	att := createTestAttestation(t, svc, requestID)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	_, err = svc.SignAttestation(att.ID, key)
	if err != nil {
		t.Fatalf("SignAttestation failed: %v", err)
	}

	// Revoke the attestation.
	err = svc.RevokeAttestation(att.ID)
	if err != nil {
		t.Fatalf("RevokeAttestation failed: %v", err)
	}

	// Attempt to verify — should fail with ErrAttestationRevoked.
	_, err = svc.VerifyAttestation(att.ID)
	if err == nil {
		t.Fatal("expected error for revoked attestation")
	}
	if !errors.Is(err, ErrAttestationRevoked) {
		t.Errorf("expected ErrAttestationRevoked, got: %v", err)
	}
}

func TestAttestationService_CreateChain(t *testing.T) {
	// Create 3 attestations where output[i] = input[i+1].
	hash1 := types.HexToHash("0x1000000000000000000000000000000000000000000000000000000000000000")
	hash2 := types.HexToHash("0x2000000000000000000000000000000000000000000000000000000000000000")
	hash3 := types.HexToHash("0x3000000000000000000000000000000000000000000000000000000000000000")
	hash4 := types.HexToHash("0x4000000000000000000000000000000000000000000000000000000000000000")

	reqID1 := types.HexToHash("0xa100000000000000000000000000000000000000000000000000000000000000")
	reqID2 := types.HexToHash("0xa200000000000000000000000000000000000000000000000000000000000000")
	reqID3 := types.HexToHash("0xa300000000000000000000000000000000000000000000000000000000000000")

	zk := &mockZKProvider{proofs: map[types.Hash][]byte{
		reqID1: {0x01},
		reqID2: {0x02},
		reqID3: {0x03},
	}}
	svc := NewAttestationService(zk, nil, time.Hour, 0)

	modelHash := testModelHash()
	operator := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Attestation 1: input=hash1, output=hash2.
	att1, err := svc.CreateAttestation(reqID1, modelHash, hash1, hash2, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 1 failed: %v", err)
	}

	// Attestation 2: input=hash2 (matches output of att1), output=hash3.
	att2, err := svc.CreateAttestation(reqID2, modelHash, hash2, hash3, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 2 failed: %v", err)
	}

	// Attestation 3: input=hash3 (matches output of att2), output=hash4.
	att3, err := svc.CreateAttestation(reqID3, modelHash, hash3, hash4, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 3 failed: %v", err)
	}

	chain, err := svc.CreateChain([]types.Hash{att1.ID, att2.ID, att3.ID})
	if err != nil {
		t.Fatalf("CreateChain failed: %v", err)
	}
	if chain.ChainID == (types.Hash{}) {
		t.Error("expected non-zero chain ID")
	}
	if len(chain.Attestations) != 3 {
		t.Errorf("chain Attestations length = %d, want 3", len(chain.Attestations))
	}
	if chain.FinalOutput != hash4 {
		t.Errorf("FinalOutput = %s, want %s", chain.FinalOutput.Hex(), hash4.Hex())
	}
}

func TestAttestationService_BrokenChain(t *testing.T) {
	hash1 := types.HexToHash("0x1000000000000000000000000000000000000000000000000000000000000000")
	hash2 := types.HexToHash("0x2000000000000000000000000000000000000000000000000000000000000000")
	hash3 := types.HexToHash("0x3000000000000000000000000000000000000000000000000000000000000000")
	hashX := types.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999") // mismatch

	reqID1 := types.HexToHash("0xb100000000000000000000000000000000000000000000000000000000000000")
	reqID2 := types.HexToHash("0xb200000000000000000000000000000000000000000000000000000000000000")

	zk := &mockZKProvider{proofs: map[types.Hash][]byte{
		reqID1: {0x01},
		reqID2: {0x02},
	}}
	svc := NewAttestationService(zk, nil, time.Hour, 0)

	modelHash := testModelHash()
	operator := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Attestation 1: input=hash1, output=hash2.
	att1, err := svc.CreateAttestation(reqID1, modelHash, hash1, hash2, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 1 failed: %v", err)
	}

	// Attestation 2: input=hashX (does NOT match output of att1 = hash2), output=hash3.
	att2, err := svc.CreateAttestation(reqID2, modelHash, hashX, hash3, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 2 failed: %v", err)
	}

	_, err = svc.CreateChain([]types.Hash{att1.ID, att2.ID})
	if err == nil {
		t.Fatal("expected error for broken chain")
	}
	if !errors.Is(err, ErrChainBroken) {
		t.Errorf("expected ErrChainBroken, got: %v", err)
	}
}

func TestAttestationService_SafetyCritical(t *testing.T) {
	requestID := testRequestID()
	svc := newTestService(requestID, time.Hour)

	modelHash := testModelHash()
	inputHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	outputHash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	operator := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// SafetyCritical without training record (zero hash) should fail.
	_, err := svc.CreateAttestation(requestID, modelHash, inputHash, outputHash, types.Hash{}, SafetyCritical, operator, "")
	if err == nil {
		t.Fatal("expected error for safety-critical without training record")
	}
	if !errors.Is(err, ErrTrainingRequired) {
		t.Errorf("expected ErrTrainingRequired, got: %v", err)
	}
}

func TestAttestationService_Prune(t *testing.T) {
	requestID1 := types.HexToHash("0xc100000000000000000000000000000000000000000000000000000000000000")
	requestID2 := types.HexToHash("0xc200000000000000000000000000000000000000000000000000000000000000")

	zk := &mockZKProvider{proofs: map[types.Hash][]byte{
		requestID1: {0x01},
		requestID2: {0x02},
	}}
	// Very short TTL for pruning test.
	svc := NewAttestationService(zk, nil, time.Millisecond, 0)

	modelHash := testModelHash()
	inputHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	outputHash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	operator := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	_, err := svc.CreateAttestation(requestID1, modelHash, inputHash, outputHash, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 1 failed: %v", err)
	}
	_, err = svc.CreateAttestation(requestID2, modelHash, inputHash, outputHash, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 2 failed: %v", err)
	}

	if svc.Count() != 2 {
		t.Fatalf("count before prune = %d, want 2", svc.Count())
	}

	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	pruned := svc.Prune()
	if pruned != 2 {
		t.Errorf("pruned = %d, want 2", pruned)
	}
	if svc.Count() != 0 {
		t.Errorf("count after prune = %d, want 0", svc.Count())
	}
}

func TestAttestationService_GetByRequest(t *testing.T) {
	requestID := testRequestID()
	svc := newTestService(requestID, time.Hour)
	att := createTestAttestation(t, svc, requestID)

	// Retrieve by request ID.
	found, ok := svc.GetByRequest(requestID)
	if !ok {
		t.Fatal("expected to find attestation by request ID")
	}
	if found.Attestation.ID != att.ID {
		t.Errorf("retrieved attestation ID = %s, want %s", found.Attestation.ID.Hex(), att.ID.Hex())
	}
	if found.Attestation.RequestID != requestID {
		t.Errorf("retrieved RequestID = %s, want %s", found.Attestation.RequestID.Hex(), requestID.Hex())
	}

	// Non-existent request ID should return false.
	unknownReq := types.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	_, ok = svc.GetByRequest(unknownReq)
	if ok {
		t.Error("expected false for unknown request ID")
	}
}

// TestAttestationService_ZeroHashRejected verifies that CreateAttestation
// rejects a zero requestID with ErrZeroHash.
func TestAttestationService_ZeroHashRejected(t *testing.T) {
	requestID := testRequestID()
	svc := newTestService(requestID, time.Hour)

	modelHash := testModelHash()
	inputHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	outputHash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	operator := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Zero requestID should be rejected.
	_, err := svc.CreateAttestation(types.Hash{}, modelHash, inputHash, outputHash, types.Hash{}, SafetyStandard, operator, "")
	if !errors.Is(err, ErrZeroHash) {
		t.Fatalf("zero requestID: expected ErrZeroHash, got: %v", err)
	}

	// Zero modelHash should be rejected.
	_, err = svc.CreateAttestation(requestID, types.Hash{}, inputHash, outputHash, types.Hash{}, SafetyStandard, operator, "")
	if !errors.Is(err, ErrZeroHash) {
		t.Fatalf("zero modelHash: expected ErrZeroHash, got: %v", err)
	}

	// Zero inputHash should be rejected.
	_, err = svc.CreateAttestation(requestID, modelHash, types.Hash{}, outputHash, types.Hash{}, SafetyStandard, operator, "")
	if !errors.Is(err, ErrZeroHash) {
		t.Fatalf("zero inputHash: expected ErrZeroHash, got: %v", err)
	}

	// Zero outputHash should be rejected.
	_, err = svc.CreateAttestation(requestID, modelHash, inputHash, types.Hash{}, types.Hash{}, SafetyStandard, operator, "")
	if !errors.Is(err, ErrZeroHash) {
		t.Fatalf("zero outputHash: expected ErrZeroHash, got: %v", err)
	}
}

// TestAttestationService_ServiceCapacity verifies that the service enforces
// maxItems and returns ErrServiceFull when capacity is reached.
func TestAttestationService_ServiceCapacity(t *testing.T) {
	reqID1 := types.HexToHash("0xd100000000000000000000000000000000000000000000000000000000000000")
	reqID2 := types.HexToHash("0xd200000000000000000000000000000000000000000000000000000000000000")
	reqID3 := types.HexToHash("0xd300000000000000000000000000000000000000000000000000000000000000")

	zk := &mockZKProvider{proofs: map[types.Hash][]byte{
		reqID1: {0x01},
		reqID2: {0x02},
		reqID3: {0x03},
	}}
	svc := NewAttestationService(zk, nil, time.Hour, 2) // maxItems=2

	modelHash := testModelHash()
	inputHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	outputHash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	operator := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// First two attestations should succeed.
	_, err := svc.CreateAttestation(reqID1, modelHash, inputHash, outputHash, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 1: %v", err)
	}
	_, err = svc.CreateAttestation(reqID2, modelHash, inputHash, outputHash, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 2: %v", err)
	}
	if svc.Count() != 2 {
		t.Fatalf("Count after 2 attestations: got %d, want 2", svc.Count())
	}

	// Third attestation should fail with ErrServiceFull.
	_, err = svc.CreateAttestation(reqID3, modelHash, inputHash, outputHash, types.Hash{}, SafetyStandard, operator, "")
	if !errors.Is(err, ErrServiceFull) {
		t.Fatalf("CreateAttestation 3: expected ErrServiceFull, got: %v", err)
	}
}

// TestAttestationService_VerifySignatureNil verifies that VerifySignature
// returns an error when given nil.
func TestAttestationService_VerifySignatureNil(t *testing.T) {
	requestID := testRequestID()
	svc := newTestService(requestID, time.Hour)

	valid, err := svc.VerifySignature(nil)
	if err == nil {
		t.Fatal("expected error for nil SignedAttestation")
	}
	if valid {
		t.Error("expected valid=false for nil SignedAttestation")
	}
}

// TestAttestationService_ConcurrentSignVerify exercises concurrent attestation
// creation and signing from multiple goroutines to verify thread safety.
func TestAttestationService_ConcurrentSignVerify(t *testing.T) {
	const numGoroutines = 5

	// Create unique request IDs and populate the ZK provider.
	proofs := make(map[types.Hash][]byte, numGoroutines)
	reqIDs := make([]types.Hash, numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		reqIDs[i] = types.BytesToHash([]byte{byte(i + 1), 0xe0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
		proofs[reqIDs[i]] = []byte{byte(i + 1), 0x02, 0x03}
	}
	zk := &mockZKProvider{proofs: proofs}
	svc := NewAttestationService(zk, nil, time.Hour, 0)

	modelHash := testModelHash()
	inputHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	outputHash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	operator := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	var wg sync.WaitGroup
	errs := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			att, cErr := svc.CreateAttestation(reqIDs[idx], modelHash, inputHash, outputHash, types.Hash{}, SafetyStandard, operator, "")
			if cErr != nil {
				errs[idx] = cErr
				return
			}

			key, kErr := crypto.GenerateKey()
			if kErr != nil {
				errs[idx] = kErr
				return
			}

			signed, sErr := svc.SignAttestation(att.ID, key)
			if sErr != nil {
				errs[idx] = sErr
				return
			}

			valid, vErr := svc.VerifySignature(signed)
			if vErr != nil {
				errs[idx] = vErr
				return
			}
			if !valid {
				errs[idx] = errors.New("signature verification failed")
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine[%d]: %v", i, err)
		}
	}

	if svc.Count() != numGoroutines {
		t.Errorf("Count = %d, want %d", svc.Count(), numGoroutines)
	}
}

// TestAttestationService_VerifyChainAfterRevoke verifies that revoking one
// attestation in a chain causes chain verification to fail.
func TestAttestationService_VerifyChainAfterRevoke(t *testing.T) {
	hash1 := types.HexToHash("0x1000000000000000000000000000000000000000000000000000000000000000")
	hash2 := types.HexToHash("0x2000000000000000000000000000000000000000000000000000000000000000")
	hash3 := types.HexToHash("0x3000000000000000000000000000000000000000000000000000000000000000")

	reqID1 := types.HexToHash("0xe100000000000000000000000000000000000000000000000000000000000000")
	reqID2 := types.HexToHash("0xe200000000000000000000000000000000000000000000000000000000000000")

	zk := &mockZKProvider{proofs: map[types.Hash][]byte{
		reqID1: {0x01},
		reqID2: {0x02},
	}}
	svc := NewAttestationService(zk, nil, time.Hour, 0)

	modelHash := testModelHash()
	operator := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Create two chained attestations.
	att1, err := svc.CreateAttestation(reqID1, modelHash, hash1, hash2, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 1: %v", err)
	}
	att2, err := svc.CreateAttestation(reqID2, modelHash, hash2, hash3, types.Hash{}, SafetyStandard, operator, "")
	if err != nil {
		t.Fatalf("CreateAttestation 2: %v", err)
	}

	// Sign both attestations.
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if _, err := svc.SignAttestation(att1.ID, key); err != nil {
		t.Fatalf("SignAttestation 1: %v", err)
	}
	if _, err := svc.SignAttestation(att2.ID, key); err != nil {
		t.Fatalf("SignAttestation 2: %v", err)
	}

	// Create the chain.
	chain, err := svc.CreateChain([]types.Hash{att1.ID, att2.ID})
	if err != nil {
		t.Fatalf("CreateChain: %v", err)
	}

	// Verify chain before revocation should succeed.
	valid, err := svc.VerifyChain(chain.ChainID)
	if err != nil {
		t.Fatalf("VerifyChain (before revoke): %v", err)
	}
	if !valid {
		t.Error("VerifyChain (before revoke): expected valid")
	}

	// Revoke the first attestation.
	if err := svc.RevokeAttestation(att1.ID); err != nil {
		t.Fatalf("RevokeAttestation: %v", err)
	}

	// Verify chain after revocation should fail with ErrAttestationRevoked.
	valid, err = svc.VerifyChain(chain.ChainID)
	if valid {
		t.Error("VerifyChain (after revoke): expected invalid")
	}
	if !errors.Is(err, ErrAttestationRevoked) {
		t.Errorf("VerifyChain (after revoke): expected ErrAttestationRevoked, got: %v", err)
	}
}
