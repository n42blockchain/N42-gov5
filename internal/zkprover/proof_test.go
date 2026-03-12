package zkprover

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestProof_EncodeDecodeRoundTrip verifies Proof survives encode→decode.
func TestProof_EncodeDecodeRoundTrip(t *testing.T) {
	proof := &Proof{
		BlockHash:    types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		BlockNumber:  12345,
		ProofData:    []byte("stark-proof-data-here"),
		PublicInputs: make([]byte, 40), // 32B stateRoot + 8B gasUsed
		Type:         ProofTypeSTARK,
	}
	copy(proof.PublicInputs[:32], proof.BlockHash[:])

	data, err := EncodeProof(proof)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeProof(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.BlockHash != proof.BlockHash {
		t.Fatal("BlockHash mismatch")
	}
	if decoded.BlockNumber != proof.BlockNumber {
		t.Fatalf("BlockNumber: got %d, want %d", decoded.BlockNumber, proof.BlockNumber)
	}
	if !bytes.Equal(decoded.ProofData, proof.ProofData) {
		t.Fatal("ProofData mismatch")
	}
	if !bytes.Equal(decoded.PublicInputs, proof.PublicInputs) {
		t.Fatal("PublicInputs mismatch")
	}
	if decoded.Type != proof.Type {
		t.Fatalf("Type: got %s, want %s", decoded.Type, proof.Type)
	}
}

// TestProof_EncodeDecodeSNARK verifies SNARK proof type round-trip.
func TestProof_EncodeDecodeSNARK(t *testing.T) {
	proof := &Proof{
		BlockNumber:  999,
		ProofData:    []byte("groth16-proof"),
		PublicInputs: []byte("public-inputs"),
		Type:         ProofTypeSNARK,
	}

	data, err := EncodeProof(proof)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeProof(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Type != ProofTypeSNARK {
		t.Fatalf("Type: got %s, want %s", decoded.Type, ProofTypeSNARK)
	}
}

// TestProof_EncodeNil verifies error on nil proof.
func TestProof_EncodeNil(t *testing.T) {
	_, err := EncodeProof(nil)
	if err == nil {
		t.Fatal("expected error for nil proof")
	}
}

// TestProof_DecodeShort verifies error on truncated data.
func TestProof_DecodeShort(t *testing.T) {
	_, err := DecodeProof(make([]byte, 10))
	if err == nil {
		t.Fatal("expected error for too-short data")
	}
}

// TestProof_DecodeTooLarge verifies MaxProofSize enforcement.
func TestProof_DecodeTooLarge(t *testing.T) {
	data := make([]byte, MaxProofSize+1)
	_, err := DecodeProof(data)
	if err == nil {
		t.Fatal("expected error for oversized proof")
	}
}

// TestProof_DecodeTruncatedProofData verifies error when proofData length
// field points beyond buffer.
func TestProof_DecodeTruncatedProofData(t *testing.T) {
	// Craft valid header but with proofDataLen pointing beyond data.
	data := make([]byte, 32+8+4)
	// Set proofDataLen to 9999.
	data[40] = 0x0F
	data[41] = 0x27

	_, err := DecodeProof(data)
	if err == nil {
		t.Fatal("expected error for truncated proof data")
	}
}

// TestProof_EmptyProofData verifies encoding with empty proof data.
func TestProof_EmptyProofData(t *testing.T) {
	proof := &Proof{
		BlockNumber:  1,
		ProofData:    []byte{},
		PublicInputs: []byte{},
		Type:         ProofTypeSTARK,
	}

	data, err := EncodeProof(proof)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeProof(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(decoded.ProofData) != 0 {
		t.Fatalf("expected empty ProofData, got %d bytes", len(decoded.ProofData))
	}
	if len(decoded.PublicInputs) != 0 {
		t.Fatalf("expected empty PublicInputs, got %d bytes", len(decoded.PublicInputs))
	}
}

// TestProof_UnknownType verifies that unknown proof types decode as STARK (default).
func TestProof_UnknownType(t *testing.T) {
	proof := &Proof{
		BlockNumber:  1,
		ProofData:    []byte("data"),
		PublicInputs: []byte("pi"),
		Type:         ProofType("unknown"),
	}

	data, err := EncodeProof(proof)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeProof(data)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Type != ProofTypeSTARK {
		t.Fatalf("expected default STARK type, got %s", decoded.Type)
	}
}

// TestProof_LargeProofData verifies round-trip with large (but within limit) proof data.
func TestProof_LargeProofData(t *testing.T) {
	proofData := make([]byte, 64*1024) // 64 KB
	for i := range proofData {
		proofData[i] = byte(i % 256)
	}

	proof := &Proof{
		BlockHash:    types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		BlockNumber:  100000,
		ProofData:    proofData,
		PublicInputs: make([]byte, 40),
		Type:         ProofTypeSTARK,
	}

	data, err := EncodeProof(proof)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeProof(data)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decoded.ProofData, proofData) {
		t.Fatal("large ProofData mismatch after round-trip")
	}
}

// TestProof_DecodePublicInputsTruncated verifies error when publicInputs
// length field points beyond buffer.
func TestProof_DecodePublicInputsTruncated(t *testing.T) {
	// Valid header + zero-length proof data + truncated public inputs len.
	buf := make([]byte, 32+8+4+4)
	// Set piLen to a large value.
	buf[44] = 0xFF
	buf[45] = 0xFF

	_, err := DecodeProof(buf)
	if err == nil {
		t.Fatal("expected error for truncated public inputs")
	}
}

// TestProof_DecodeProofDataLenExceedsMaxProofSize verifies MaxProofSize check per field.
func TestProof_DecodeProofDataLenExceedsMaxProofSize(t *testing.T) {
	// Craft valid header but proofDataLen > MaxProofSize.
	buf := make([]byte, 32+8+4+1)
	// Set proofDataLen to MaxProofSize + 1 (but total buf is small).
	binary.LittleEndian.PutUint32(buf[40:44], uint32(MaxProofSize)+1)

	_, err := DecodeProof(buf)
	if err == nil {
		t.Fatal("expected error for proof data length exceeding MaxProofSize")
	}
}

// TestProof_DecodePublicInputsLenExceedsMaxProofSize verifies MaxProofSize per PI field.
func TestProof_DecodePublicInputsLenExceedsMaxProofSize(t *testing.T) {
	// Valid header + 0-length proofData + piLen > MaxProofSize.
	buf := make([]byte, 32+8+4+4+1)
	binary.LittleEndian.PutUint32(buf[40:44], 0)                 // proofDataLen = 0
	binary.LittleEndian.PutUint32(buf[44:48], uint32(MaxProofSize)+1) // piLen > max

	_, err := DecodeProof(buf)
	if err == nil {
		t.Fatal("expected error for public inputs length exceeding MaxProofSize")
	}
}

// TestProof_DecodeMinimalValid verifies decoding the minimal valid proof.
func TestProof_DecodeMinimalValid(t *testing.T) {
	proof := &Proof{
		ProofData:    []byte{},
		PublicInputs: []byte{},
		Type:         ProofTypeSTARK,
	}

	data, err := EncodeProof(proof)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeProof(data)
	if err != nil {
		t.Fatalf("minimal valid proof should decode: %v", err)
	}
	if decoded.BlockNumber != 0 {
		t.Fatalf("expected BlockNumber 0, got %d", decoded.BlockNumber)
	}
	if decoded.Type != ProofTypeSTARK {
		t.Fatalf("expected STARK, got %s", decoded.Type)
	}
}

// TestProof_DecodeProofTypeMissing verifies error when proof type byte is absent.
func TestProof_DecodeProofTypeMissing(t *testing.T) {
	// Valid header + 0 proofData + 0 publicInputs, but NO type byte.
	proof := &Proof{
		ProofData:    []byte{},
		PublicInputs: []byte{},
		Type:         ProofTypeSTARK,
	}
	data, err := EncodeProof(proof)
	if err != nil {
		t.Fatal(err)
	}
	// Remove last byte (type byte).
	truncated := data[:len(data)-1]

	_, err = DecodeProof(truncated)
	if err == nil {
		t.Fatal("expected error for missing proof type byte")
	}
}
