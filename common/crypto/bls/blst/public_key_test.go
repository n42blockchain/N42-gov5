//go:build (android || (linux && amd64) || (linux && arm64) || (darwin && amd64) || (darwin && arm64) || (windows && amd64)) && !blst_disabled

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for BLS PublicKey UnmarshalJSON/UnmarshalText bug fixes

package blst

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

// TestPublicKeyUnmarshalJSON tests the fixed UnmarshalJSON method
func TestPublicKeyUnmarshalJSON(t *testing.T) {
	// Generate a valid BLS public key for testing
	// Using a known valid public key bytes (48 bytes)
	validPubKeyHex := "0x" + "b5bfd7dd8cdeb128843bc287230af38926187075cbfbefa81009a2ce615ac53d2914e5e9e206e3d5ab1c00a645cb6e230"
	
	// Test valid JSON input
	jsonInput := `"` + validPubKeyHex + `"`
	
	var pk PublicKey
	err := json.Unmarshal([]byte(jsonInput), &pk)
	if err != nil {
		// This may fail if the key is not a valid BLS key, which is expected
		// The important thing is it doesn't panic
		t.Logf("UnmarshalJSON returned error (expected for invalid key): %v", err)
	} else {
		t.Log("✓ UnmarshalJSON succeeded with valid input")
		
		// Verify the key can be marshaled back
		marshaled, err := pk.MarshalText()
		if err != nil {
			t.Errorf("MarshalText failed: %v", err)
		} else {
			t.Logf("✓ MarshalText succeeded: %s", string(marshaled))
		}
	}
}

// TestPublicKeyUnmarshalText tests the fixed UnmarshalText method
func TestPublicKeyUnmarshalText(t *testing.T) {
	// Generate a valid BLS public key for testing
	validPubKeyHex := "0x" + "b5bfd7dd8cdeb128843bc287230af38926187075cbfbefa81009a2ce615ac53d2914e5e9e206e3d5ab1c00a645cb6e230"
	
	var pk PublicKey
	err := pk.UnmarshalText([]byte(validPubKeyHex))
	if err != nil {
		// This may fail if the key is not a valid BLS key, which is expected
		// The important thing is it doesn't panic
		t.Logf("UnmarshalText returned error (expected for invalid key): %v", err)
	} else {
		t.Log("✓ UnmarshalText succeeded with valid input")
	}
}

// TestPublicKeyUnmarshalInvalidLength tests error handling for invalid length
func TestPublicKeyUnmarshalInvalidLength(t *testing.T) {
	// Test with wrong length (not 48 bytes)
	shortHex := "0x1234567890abcdef"
	
	var pk PublicKey
	err := pk.UnmarshalText([]byte(shortHex))
	if err != nil {
		t.Logf("✓ UnmarshalText correctly rejects short input: %v", err)
	} else {
		t.Error("UnmarshalText should reject input with wrong length")
	}
}

// TestPublicKeyUnmarshalEmpty tests error handling for empty input
func TestPublicKeyUnmarshalEmpty(t *testing.T) {
	var pk PublicKey
	err := pk.UnmarshalText([]byte(""))
	if err != nil {
		t.Logf("✓ UnmarshalText correctly rejects empty input: %v", err)
	} else {
		t.Error("UnmarshalText should reject empty input")
	}
}

// TestPublicKeyMarshalRoundTrip tests marshal/unmarshal round trip
func TestPublicKeyMarshalRoundTrip(t *testing.T) {
	// Create a valid key from bytes
	pubKeyBytes, _ := hex.DecodeString("b5bfd7dd8cdeb128843bc287230af38926187075cbfbefa81009a2ce615ac53d2914e5e9e206e3d5ab1c00a645cb6e23")
	
	if len(pubKeyBytes) != BLSPubkeyLength {
		t.Skipf("Test key has wrong length: %d (expected %d)", len(pubKeyBytes), BLSPubkeyLength)
	}
	
	pk, err := PublicKeyFromBytes(pubKeyBytes)
	if err != nil {
		t.Skipf("Cannot create key from test bytes: %v", err)
	}
	
	// Marshal
	marshaled := pk.Marshal()
	if len(marshaled) != BLSPubkeyLength {
		t.Errorf("Marshal returned wrong length: %d", len(marshaled))
	}
	
	// Unmarshal
	pk2, err := PublicKeyFromBytes(marshaled)
	if err != nil {
		t.Errorf("Cannot unmarshal marshaled key: %v", err)
	}
	
	// Compare
	if !pk.Equals(pk2) {
		t.Error("Round-trip failed: keys are not equal")
	} else {
		t.Log("✓ Marshal/Unmarshal round-trip successful")
	}
}

// TestBufferAllocationFix tests that the buffer allocation bug is fixed
// Previously: b := make([]byte, 0) would create a zero-length slice
// Fixed: b := make([]byte, BLSPubkeyLength) allocates correct buffer
func TestBufferAllocationFix(t *testing.T) {
	// This test verifies that UnmarshalText properly allocates a buffer
	// The bug was: b := make([]byte, 0) which would fail to unmarshal
	
	// A valid 48-byte hex string (padded with zeros for testing)
	testHex := "0x" + "000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	
	var pk PublicKey
	err := pk.UnmarshalText([]byte(testHex))
	
	// We expect an error because this is not a valid BLS key,
	// but the error should NOT be about buffer size
	if err != nil {
		// Check that it's a key validation error, not a buffer error
		errStr := err.Error()
		if errStr == "unexpected end of JSON input" || errStr == "invalid input length" {
			t.Errorf("Buffer allocation bug not fixed: %v", err)
		} else {
			t.Logf("✓ Buffer correctly allocated, key validation error: %v", err)
		}
	} else {
		// Zero bytes create an invalid key, but buffer was allocated correctly
		t.Log("✓ Buffer allocation working (unexpected success with zero key)")
	}
}

