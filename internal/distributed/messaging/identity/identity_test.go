// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package identity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/n42blockchain/N42/crypto"
)

func TestCreateDID(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	doc, err := CreateDID(key)
	if err != nil {
		t.Fatalf("CreateDID: %v", err)
	}

	if !strings.HasPrefix(doc.ID, DIDPrefix) {
		t.Errorf("DID = %q, want prefix %q", doc.ID, DIDPrefix)
	}

	addr := crypto.PubkeyToAddress(key.PublicKey)
	expectedSuffix := strings.ToLower(addr.Hex())
	if !strings.HasSuffix(doc.ID, expectedSuffix) {
		t.Errorf("DID = %q, want suffix %q", doc.ID, expectedSuffix)
	}

	if len(doc.VerificationMethod) != 1 {
		t.Errorf("VerificationMethod count = %d, want 1", len(doc.VerificationMethod))
	}
	if len(doc.Authentication) != 1 {
		t.Errorf("Authentication count = %d, want 1", len(doc.Authentication))
	}
}

func TestParseDID(t *testing.T) {
	// Valid DID
	validDID := "did:n42:0x1234567890abcdef1234567890abcdef12345678"
	addr, err := ParseDID(validDID)
	if err != nil {
		t.Fatalf("ParseDID valid: %v", err)
	}
	if addr != "0x1234567890abcdef1234567890abcdef12345678" {
		t.Errorf("address = %q, want full address", addr)
	}

	// Invalid prefix
	_, err = ParseDID("did:other:0x1234567890abcdef1234567890abcdef12345678")
	if err == nil {
		t.Error("expected error for invalid prefix")
	}

	// Invalid address (too short)
	_, err = ParseDID("did:n42:0x1234")
	if err == nil {
		t.Error("expected error for short address")
	}

	// Missing 0x prefix
	_, err = ParseDID("did:n42:1234567890abcdef1234567890abcdef1234567890")
	if err == nil {
		t.Error("expected error for address without 0x prefix")
	}
}

func TestDIDFromAddress(t *testing.T) {
	addr := "0xAbCdEf1234567890AbCdEf1234567890AbCdEf12"
	did := DIDFromAddress(addr)
	expected := "did:n42:" + strings.ToLower(addr)
	if did != expected {
		t.Errorf("DIDFromAddress = %q, want %q", did, expected)
	}
}

func TestSignAndVerifyDID(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	doc, err := CreateDID(key)
	if err != nil {
		t.Fatalf("CreateDID: %v", err)
	}

	message := []byte("test message to sign")

	sig, err := SignWithDID(message, key)
	if err != nil {
		t.Fatalf("SignWithDID: %v", err)
	}

	valid, err := VerifyDIDSignature(doc, message, sig)
	if err != nil {
		t.Fatalf("VerifyDIDSignature: %v", err)
	}
	if !valid {
		t.Error("expected valid signature")
	}
}

func TestVerifyDIDSignatureBadSig(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	doc, err := CreateDID(key)
	if err != nil {
		t.Fatalf("CreateDID: %v", err)
	}

	message := []byte("test message")

	sig, err := SignWithDID(message, key)
	if err != nil {
		t.Fatalf("SignWithDID: %v", err)
	}

	// Tamper with the signature
	sig[10] ^= 0xFF
	sig[20] ^= 0xFF

	valid, err := VerifyDIDSignature(doc, message, sig)
	if err != nil {
		t.Fatalf("VerifyDIDSignature error: %v", err)
	}
	if valid {
		t.Error("expected invalid signature after tampering")
	}
}

func TestVerifyDIDSignatureShort(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	doc, err := CreateDID(key)
	if err != nil {
		t.Fatalf("CreateDID: %v", err)
	}

	shortSig := make([]byte, 32) // less than 64 bytes
	_, err = VerifyDIDSignature(doc, []byte("msg"), shortSig)
	if err == nil {
		t.Error("expected error for signature shorter than 64 bytes")
	}
}

func TestSerializeDeserializeDIDDocument(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	doc, err := CreateDID(key)
	if err != nil {
		t.Fatalf("CreateDID: %v", err)
	}

	data, err := SerializeDIDDocument(doc)
	if err != nil {
		t.Fatalf("SerializeDIDDocument: %v", err)
	}

	// Verify it's valid JSON
	if !json.Valid(data) {
		t.Error("serialized document is not valid JSON")
	}

	restored, err := DeserializeDIDDocument(data)
	if err != nil {
		t.Fatalf("DeserializeDIDDocument: %v", err)
	}

	if restored.ID != doc.ID {
		t.Errorf("ID = %q, want %q", restored.ID, doc.ID)
	}
	if restored.Controller != doc.Controller {
		t.Errorf("Controller = %q, want %q", restored.Controller, doc.Controller)
	}
	if len(restored.VerificationMethod) != len(doc.VerificationMethod) {
		t.Errorf("VerificationMethod count = %d, want %d",
			len(restored.VerificationMethod), len(doc.VerificationMethod))
	}
	if len(restored.Authentication) != len(doc.Authentication) {
		t.Errorf("Authentication count = %d, want %d",
			len(restored.Authentication), len(doc.Authentication))
	}
	if len(restored.Service) != len(doc.Service) {
		t.Errorf("Service count = %d, want %d",
			len(restored.Service), len(doc.Service))
	}
}

func TestDIDResolverRegisterResolve(t *testing.T) {
	resolver := NewDIDResolver(10, time.Hour)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	doc, err := CreateDID(key)
	if err != nil {
		t.Fatalf("CreateDID: %v", err)
	}

	if err := resolver.Register(doc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	resolved, err := resolver.Resolve(doc.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.ID != doc.ID {
		t.Errorf("resolved ID = %q, want %q", resolved.ID, doc.ID)
	}
}

func TestDIDResolverNotFound(t *testing.T) {
	resolver := NewDIDResolver(10, time.Hour)

	_, err := resolver.Resolve("did:n42:0x0000000000000000000000000000000000000000")
	if err == nil {
		t.Error("expected error for unregistered DID")
	}
}

func TestDIDResolverUpdate(t *testing.T) {
	resolver := NewDIDResolver(10, time.Hour)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	doc, err := CreateDID(key)
	if err != nil {
		t.Fatalf("CreateDID: %v", err)
	}

	if err := resolver.Register(doc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Update with a new service endpoint
	doc.Service = append(doc.Service, ServiceEndpoint{
		ID:              doc.ID + "#storage",
		Type:            "N42Storage",
		ServiceEndpoint: "n42://storage",
	})

	if err := resolver.Update(doc); err != nil {
		t.Fatalf("Update: %v", err)
	}

	resolved, err := resolver.Resolve(doc.ID)
	if err != nil {
		t.Fatalf("Resolve after update: %v", err)
	}

	if len(resolved.Service) != 2 {
		t.Errorf("Service count = %d, want 2", len(resolved.Service))
	}
}

func TestDIDResolverDeactivate(t *testing.T) {
	resolver := NewDIDResolver(10, time.Hour)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	doc, err := CreateDID(key)
	if err != nil {
		t.Fatalf("CreateDID: %v", err)
	}

	if err := resolver.Register(doc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Verify it resolves
	if _, err := resolver.Resolve(doc.ID); err != nil {
		t.Fatalf("Resolve before deactivate: %v", err)
	}

	if err := resolver.Deactivate(doc.ID); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}

	_, err = resolver.Resolve(doc.ID)
	if err == nil {
		t.Error("expected error after deactivation")
	}
}

func TestDIDResolverCacheEviction(t *testing.T) {
	resolver := NewDIDResolver(2, time.Hour)

	// Register 3 DIDs, cache size is 2 so one should be evicted
	var dids []string
	for i := 0; i < 3; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey %d: %v", i, err)
		}
		doc, err := CreateDID(key)
		if err != nil {
			t.Fatalf("CreateDID %d: %v", i, err)
		}
		if err := resolver.Register(doc); err != nil {
			t.Fatalf("Register %d: %v", i, err)
		}
		dids = append(dids, doc.ID)
	}

	// All 3 should still resolve from registry (even if evicted from cache)
	for i, did := range dids {
		_, err := resolver.Resolve(did)
		if err != nil {
			t.Errorf("Resolve DID %d: %v", i, err)
		}
	}

	// Verify cache size is at most 2
	resolver.mu.RLock()
	cacheLen := len(resolver.cache)
	resolver.mu.RUnlock()
	if cacheLen > 2 {
		t.Errorf("cache size = %d, want <= 2", cacheLen)
	}

	if resolver.Size() != 3 {
		t.Errorf("registry size = %d, want 3", resolver.Size())
	}
}

func TestDIDResolverInvalidDID(t *testing.T) {
	resolver := NewDIDResolver(10, time.Hour)

	// Invalid format
	_, err := resolver.Resolve("invalid-did")
	if err == nil {
		t.Error("expected error for invalid DID format")
	}

	// Wrong method
	_, err = resolver.Resolve("did:other:0x1234567890abcdef1234567890abcdef12345678")
	if err == nil {
		t.Error("expected error for wrong DID method")
	}
}
