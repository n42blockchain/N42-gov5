package storage

import (
	"encoding/hex"
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
