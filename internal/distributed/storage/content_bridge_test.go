package storage

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestKeccak256ToCIDv1(t *testing.T) {
	hash, _ := hex.DecodeString("abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	cid := Keccak256ToCIDv1(hash)
	if cid == "" {
		t.Fatal("expected non-empty CID")
	}
	t.Logf("CID: %s", cid)

	// Round-trip
	recovered := CIDv1ToKeccak256(cid)
	if len(recovered) != 32 {
		t.Fatalf("recovered hash len = %d, want 32", len(recovered))
	}
	for i := range hash {
		if hash[i] != recovered[i] {
			t.Fatalf("hash mismatch at byte %d", i)
		}
	}
}

func TestKeccak256ToCIDv1InvalidInput(t *testing.T) {
	// Too short
	cid := Keccak256ToCIDv1([]byte{1, 2, 3})
	if cid != "" {
		t.Fatal("expected empty CID for short input")
	}
}

func TestCIDv1ToKeccak256Invalid(t *testing.T) {
	tests := []string{
		"",
		"invalid",
		"f01551b20", // prefix but no hash
		"f01551b20zzzz", // invalid hex
		"QmYwAPJzv5CZsnN625s3XfXYmPXyMeLoZFyRzhLqCVPvP8", // CIDv0 (not keccak256)
	}
	for _, cid := range tests {
		result := CIDv1ToKeccak256(cid)
		if result != nil {
			t.Errorf("CIDv1ToKeccak256(%q) should return nil", cid)
		}
	}
}

func TestContentBridge(t *testing.T) {
	bridge := NewContentBridge(nil, false)
	hash, _ := hex.DecodeString("1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	cid := bridge.HashToCID(hash)
	if cid == "" {
		t.Fatal("expected CID")
	}

	recovered := bridge.CIDToHash(cid)
	if len(recovered) != 32 {
		t.Fatal("expected 32-byte hash")
	}
}

func TestKeccak256ToCIDv1KnownHash(t *testing.T) {
	hash, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000001")
	cid := Keccak256ToCIDv1(hash)
	if !strings.HasPrefix(cid, "f01551b20") {
		t.Fatalf("CID should start with 'f01551b20', got %q", cid)
	}
	// The hex portion after the prefix should match the input hash.
	expected := "f01551b20" + hex.EncodeToString(hash)
	if cid != expected {
		t.Fatalf("CID = %q, want %q", cid, expected)
	}
}

func TestCIDv1ToKeccak256InvalidCID(t *testing.T) {
	invalids := []string{
		"",
		"abc",
		"f01551b20",                                                               // prefix only, no hash
		"f01551b20gg00000000000000000000000000000000000000000000000000000000000000", // non-hex after prefix
		"x01551b200000000000000000000000000000000000000000000000000000000000000000", // wrong prefix start
	}
	for _, cid := range invalids {
		result := CIDv1ToKeccak256(cid)
		if result != nil {
			t.Errorf("CIDv1ToKeccak256(%q) should return nil, got %x", cid, result)
		}
	}
}

func TestContentBridgeRoundTrip(t *testing.T) {
	bridge := NewContentBridge(nil, false)

	hashes := []string{
		"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		"0000000000000000000000000000000000000000000000000000000000000000",
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}

	for _, hashHex := range hashes {
		hash, _ := hex.DecodeString(hashHex)
		cid := bridge.HashToCID(hash)
		if cid == "" {
			t.Fatalf("HashToCID returned empty for %s", hashHex)
		}
		recovered := bridge.CIDToHash(cid)
		if !bytes.Equal(hash, recovered) {
			t.Fatalf("round-trip failed for %s: recovered %x", hashHex, recovered)
		}
	}
}

func TestContentBridgeEmptyHash(t *testing.T) {
	bridge := NewContentBridge(nil, false)

	// Zero hash (all zeros) is still a valid 32-byte hash.
	zeroHash := make([]byte, 32)
	cid := bridge.HashToCID(zeroHash)
	if cid == "" {
		t.Fatal("expected valid CID for zero hash")
	}
	if !strings.HasPrefix(cid, "f01551b20") {
		t.Fatalf("CID format unexpected: %s", cid)
	}

	// Should round-trip.
	recovered := bridge.CIDToHash(cid)
	if !bytes.Equal(zeroHash, recovered) {
		t.Fatalf("zero hash round-trip failed: got %x", recovered)
	}
}
