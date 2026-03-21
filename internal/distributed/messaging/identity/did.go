// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package identity implements W3C DID v1.1 decentralized identity for the
// messaging system, binding wallet addresses to DID documents.
// DID method: did:n42:<address>
package identity

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"golang.org/x/crypto/sha3"
)

const (
	// DIDMethod is the N42 DID method name.
	DIDMethod = "n42"

	// DIDPrefix is the full DID prefix.
	DIDPrefix = "did:n42:"
)

// DIDDocument represents a W3C DID v1.1 document.
type DIDDocument struct {
	Context            []string             `json:"@context"`
	ID                 string               `json:"id"`
	Controller         string               `json:"controller,omitempty"`
	VerificationMethod []VerificationMethod `json:"verificationMethod"`
	Authentication     []string             `json:"authentication"`
	KeyAgreement       []string             `json:"keyAgreement,omitempty"`
	Service            []ServiceEndpoint    `json:"service,omitempty"`
	Created            time.Time            `json:"created"`
	Updated            time.Time            `json:"updated"`
}

// VerificationMethod describes a cryptographic key in a DID document.
type VerificationMethod struct {
	ID                  string `json:"id"`
	Type                string `json:"type"`
	Controller          string `json:"controller"`
	PublicKeyHex        string `json:"publicKeyHex,omitempty"`
	BlockchainAccountID string `json:"blockchainAccountId,omitempty"`
}

// ServiceEndpoint describes a service in a DID document.
type ServiceEndpoint struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint string `json:"serviceEndpoint"`
}

// CreateDID creates a DID document from a wallet private key.
// The DID is derived from the Ethereum address: did:n42:<address>
func CreateDID(walletKey *ecdsa.PrivateKey) (*DIDDocument, error) {
	if walletKey == nil {
		return nil, errors.New("nil wallet key")
	}

	addr := crypto.PubkeyToAddress(walletKey.PublicKey)
	did := DIDPrefix + strings.ToLower(addr.Hex())
	pubKeyHex := hex.EncodeToString(crypto.CompressPubkey(&walletKey.PublicKey))

	now := time.Now()
	doc := &DIDDocument{
		Context: []string{
			"https://www.w3.org/ns/did/v1",
			"https://w3id.org/security/suites/secp256k1-2019/v1",
		},
		ID:         did,
		Controller: did,
		VerificationMethod: []VerificationMethod{
			{
				ID:                  did + "#key-1",
				Type:                "EcdsaSecp256k1VerificationKey2019",
				Controller:          did,
				PublicKeyHex:        pubKeyHex,
				BlockchainAccountID: fmt.Sprintf("eip155:1:%s", addr.Hex()),
			},
		},
		Authentication: []string{did + "#key-1"},
		Service: []ServiceEndpoint{
			{
				ID:              did + "#messaging",
				Type:            "N42Messaging",
				ServiceEndpoint: "n42://messaging",
			},
		},
		Created: now,
		Updated: now,
	}

	return doc, nil
}

// ParseDID parses a DID string and extracts the address.
func ParseDID(did string) (string, error) {
	if !strings.HasPrefix(did, DIDPrefix) {
		return "", fmt.Errorf("invalid DID method: expected prefix %s", DIDPrefix)
	}
	addr := did[len(DIDPrefix):]
	if len(addr) != 42 || !strings.HasPrefix(addr, "0x") {
		return "", fmt.Errorf("invalid address in DID: %s", addr)
	}
	return addr, nil
}

// DIDFromAddress creates a DID string from an Ethereum address.
func DIDFromAddress(address string) string {
	return DIDPrefix + strings.ToLower(address)
}

// VerifyDIDSignature verifies a signature against a DID document's verification method.
func VerifyDIDSignature(doc *DIDDocument, message []byte, signature []byte) (bool, error) {
	if doc == nil {
		return false, errors.New("nil DID document")
	}
	if len(doc.VerificationMethod) == 0 {
		return false, errors.New("no verification methods")
	}
	if len(signature) < 64 {
		return false, errors.New("signature too short")
	}

	// Hash the message
	h := sha3.NewLegacyKeccak256()
	h.Write(message)
	hash := h.Sum(nil)

	for _, vm := range doc.VerificationMethod {
		if vm.PublicKeyHex == "" {
			continue
		}
		pubKeyBytes, err := hex.DecodeString(vm.PublicKeyHex)
		if err != nil {
			continue
		}
		pubKey, err := crypto.DecompressPubkey(pubKeyBytes)
		if err != nil {
			continue
		}
		if crypto.VerifySignature(
			crypto.CompressPubkey(pubKey),
			hash,
			signature[:64], // strip recovery ID
		) {
			return true, nil
		}
	}

	return false, nil
}

// SignWithDID signs a message using the wallet key associated with a DID.
func SignWithDID(message []byte, walletKey *ecdsa.PrivateKey) ([]byte, error) {
	h := sha3.NewLegacyKeccak256()
	h.Write(message)
	hash := h.Sum(nil)
	return crypto.Sign(hash, walletKey)
}

// SerializeDIDDocument serializes a DID document to JSON.
func SerializeDIDDocument(doc *DIDDocument) ([]byte, error) {
	return json.Marshal(doc)
}

// DeserializeDIDDocument deserializes a DID document from JSON.
func DeserializeDIDDocument(data []byte) (*DIDDocument, error) {
	var doc DIDDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal DID document: %w", err)
	}
	return &doc, nil
}
