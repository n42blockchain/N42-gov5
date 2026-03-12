package zkverifier

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/zkprover"
)

// makePublicInputs constructs valid STARK public inputs [32B stateRoot][8B gasUsed].
func makePublicInputs(stateRoot types.Hash, gasUsed uint64) []byte {
	pi := make([]byte, 40)
	copy(pi[:32], stateRoot[:])
	binary.LittleEndian.PutUint64(pi[32:], gasUsed)
	return pi
}

// TestVerifier_Verify_STARK_Valid verifies a valid STARK proof passes.
func TestVerifier_Verify_STARK_Valid(t *testing.T) {
	v := NewVerifier()
	stateRoot := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	proof := &zkprover.Proof{
		BlockNumber:  100,
		ProofData:    []byte("valid-stark-proof-data"),
		PublicInputs: makePublicInputs(stateRoot, 21000),
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.Verify(proof, stateRoot, 21000)
	if err != nil {
		t.Fatalf("valid STARK proof should verify: %v", err)
	}
}

// TestVerifier_Verify_STARK_StateRootMismatch verifies detection of
// mismatched state root.
func TestVerifier_Verify_STARK_StateRootMismatch(t *testing.T) {
	v := NewVerifier()
	provenRoot := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	expectedRoot := types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	proof := &zkprover.Proof{
		BlockNumber:  100,
		ProofData:    []byte("stark-proof-data"),
		PublicInputs: makePublicInputs(provenRoot, 21000),
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.Verify(proof, expectedRoot, 21000)
	if err == nil {
		t.Fatal("expected error for state root mismatch")
	}
}

// TestVerifier_Verify_STARK_PublicInputsTooShort verifies rejection of
// truncated public inputs.
func TestVerifier_Verify_STARK_PublicInputsTooShort(t *testing.T) {
	v := NewVerifier()
	proof := &zkprover.Proof{
		BlockNumber:  100,
		ProofData:    []byte("data"),
		PublicInputs: make([]byte, 20), // too short (need 40)
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.Verify(proof, types.Hash{}, 21000)
	if err == nil {
		t.Fatal("expected error for too-short public inputs")
	}
}

// TestVerifier_Verify_SNARK_Valid verifies a valid SNARK proof passes.
func TestVerifier_Verify_SNARK_Valid(t *testing.T) {
	v := NewVerifier()
	stateRoot := types.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")

	pi := make([]byte, 32)
	copy(pi, stateRoot[:])

	proof := &zkprover.Proof{
		BlockNumber:  200,
		ProofData:    []byte("groth16-proof-data"),
		PublicInputs: pi,
		Type:         zkprover.ProofTypeSNARK,
	}

	err := v.Verify(proof, stateRoot, 0)
	if err != nil {
		t.Fatalf("valid SNARK proof should verify: %v", err)
	}
}

// TestVerifier_Verify_SNARK_StateRootMismatch verifies SNARK root check.
func TestVerifier_Verify_SNARK_StateRootMismatch(t *testing.T) {
	v := NewVerifier()
	provenRoot := types.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	expectedRoot := types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")

	pi := make([]byte, 32)
	copy(pi, provenRoot[:])

	proof := &zkprover.Proof{
		BlockNumber:  200,
		ProofData:    []byte("groth16-proof"),
		PublicInputs: pi,
		Type:         zkprover.ProofTypeSNARK,
	}

	err := v.Verify(proof, expectedRoot, 0)
	if err == nil {
		t.Fatal("expected error for SNARK state root mismatch")
	}
}

// TestVerifier_Verify_SNARK_PublicInputsTooShort verifies SNARK rejects
// truncated public inputs.
func TestVerifier_Verify_SNARK_PublicInputsTooShort(t *testing.T) {
	v := NewVerifier()
	proof := &zkprover.Proof{
		BlockNumber:  200,
		ProofData:    []byte("data"),
		PublicInputs: make([]byte, 16), // too short (need 32)
		Type:         zkprover.ProofTypeSNARK,
	}

	err := v.Verify(proof, types.Hash{}, 0)
	if err == nil {
		t.Fatal("expected error for too-short SNARK public inputs")
	}
}

// TestVerifier_Verify_NilProof verifies error on nil proof.
func TestVerifier_Verify_NilProof(t *testing.T) {
	v := NewVerifier()
	err := v.Verify(nil, types.Hash{}, 0)
	if err != ErrProofNil {
		t.Fatalf("expected ErrProofNil, got %v", err)
	}
}

// TestVerifier_Verify_EmptyProofData verifies error on empty proof data.
func TestVerifier_Verify_EmptyProofData(t *testing.T) {
	v := NewVerifier()
	proof := &zkprover.Proof{
		BlockNumber:  100,
		ProofData:    nil,
		PublicInputs: make([]byte, 40),
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.Verify(proof, types.Hash{}, 0)
	if err != ErrProofDataEmpty {
		t.Fatalf("expected ErrProofDataEmpty, got %v", err)
	}
}

// TestVerifier_Verify_UnsupportedType verifies error on unknown proof type.
func TestVerifier_Verify_UnsupportedType(t *testing.T) {
	v := NewVerifier()
	proof := &zkprover.Proof{
		BlockNumber:  100,
		ProofData:    []byte("data"),
		PublicInputs: make([]byte, 40),
		Type:         zkprover.ProofType("plonk"),
	}

	err := v.Verify(proof, types.Hash{}, 0)
	if err == nil {
		t.Fatal("expected error for unsupported proof type")
	}
}

// TestVerifier_Verify_STARK_ZeroStateRoot verifies STARK passes with
// matching zero root.
func TestVerifier_Verify_STARK_ZeroStateRoot(t *testing.T) {
	v := NewVerifier()
	zeroRoot := types.Hash{}

	proof := &zkprover.Proof{
		BlockNumber:  1,
		ProofData:    []byte("proof"),
		PublicInputs: makePublicInputs(zeroRoot, 0),
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.Verify(proof, zeroRoot, 0)
	if err != nil {
		t.Fatalf("zero root proof should verify: %v", err)
	}
}

// TestVerifier_VerifySTARK_Direct tests the VerifySTARK method directly.
func TestVerifier_VerifySTARK_Direct(t *testing.T) {
	v := NewVerifier()
	root := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	proof := &zkprover.Proof{
		ProofData:    []byte("stark-data"),
		PublicInputs: makePublicInputs(root, 50000),
	}

	err := v.VerifySTARK(proof, root, 50000)
	if err != nil {
		t.Fatalf("VerifySTARK should pass: %v", err)
	}
}

// TestVerifier_STARK_GasUsedMismatch verifies gasUsed mismatch detection.
func TestVerifier_STARK_GasUsedMismatch(t *testing.T) {
	v := NewVerifier()
	root := types.Hash{0x01}

	proof := &zkprover.Proof{
		BlockNumber:  100,
		ProofData:    []byte("data"),
		PublicInputs: makePublicInputs(root, 21000),
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.Verify(proof, root, 42000) // different gasUsed
	if err == nil {
		t.Fatal("expected error for gas used mismatch")
	}
}

// TestVerifier_STARK_GasUsedZero verifies zero gasUsed matches.
func TestVerifier_STARK_GasUsedZero(t *testing.T) {
	v := NewVerifier()
	root := types.Hash{0x01}

	proof := &zkprover.Proof{
		BlockNumber:  1,
		ProofData:    []byte("data"),
		PublicInputs: makePublicInputs(root, 0),
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.Verify(proof, root, 0)
	if err != nil {
		t.Fatalf("zero gasUsed should match: %v", err)
	}
}

// TestVerifier_STARK_GasUsedMaxUint64 verifies max uint64 gasUsed.
func TestVerifier_STARK_GasUsedMaxUint64(t *testing.T) {
	v := NewVerifier()
	root := types.Hash{0x01}
	maxGas := uint64(^uint64(0))

	proof := &zkprover.Proof{
		BlockNumber:  1,
		ProofData:    []byte("data"),
		PublicInputs: makePublicInputs(root, maxGas),
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.Verify(proof, root, maxGas)
	if err != nil {
		t.Fatalf("max gasUsed should match: %v", err)
	}
}

// TestVerifier_STARK_PublicInputsExact40 verifies exact 40-byte boundary.
func TestVerifier_STARK_PublicInputsExact40(t *testing.T) {
	v := NewVerifier()
	root := types.Hash{0x01}

	proof := &zkprover.Proof{
		BlockNumber:  1,
		ProofData:    []byte("data"),
		PublicInputs: makePublicInputs(root, 21000), // exactly 40 bytes
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.VerifySTARK(proof, root, 21000)
	if err != nil {
		t.Fatalf("40 bytes should be valid: %v", err)
	}
}

// TestVerifier_STARK_PublicInputs39Bytes verifies 39-byte boundary rejection.
func TestVerifier_STARK_PublicInputs39Bytes(t *testing.T) {
	v := NewVerifier()
	proof := &zkprover.Proof{
		ProofData:    []byte("data"),
		PublicInputs: make([]byte, 39),
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.VerifySTARK(proof, types.Hash{}, 0)
	if err == nil {
		t.Fatal("expected error for 39-byte public inputs")
	}
}

// TestVerifier_STARK_PublicInputsExtraData verifies 48 bytes is accepted.
func TestVerifier_STARK_PublicInputsExtraData(t *testing.T) {
	v := NewVerifier()
	root := types.Hash{0x01}

	pi := make([]byte, 48)
	copy(pi[:32], root[:])
	binary.LittleEndian.PutUint64(pi[32:], 21000)

	proof := &zkprover.Proof{
		ProofData:    []byte("data"),
		PublicInputs: pi,
		Type:         zkprover.ProofTypeSTARK,
	}

	err := v.VerifySTARK(proof, root, 21000)
	if err != nil {
		t.Fatalf("48-byte public inputs should verify: %v", err)
	}
}

// TestVerifier_VerifySNARK_Direct tests the VerifySNARK method directly.
func TestVerifier_VerifySNARK_Direct(t *testing.T) {
	v := NewVerifier()
	root := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	pi := make([]byte, 32)
	copy(pi, root[:])

	proof := &zkprover.Proof{
		ProofData:    []byte("snark-data"),
		PublicInputs: pi,
	}

	err := v.VerifySNARK(proof, root)
	if err != nil {
		t.Fatalf("VerifySNARK should pass: %v", err)
	}
}
