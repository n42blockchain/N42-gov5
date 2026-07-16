// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Device-side BLS identity lifecycle for the mobile attestation SDK. A phone
// generates its own BLS key on first launch, persists the 32-byte secret in
// platform secure storage (Keychain / Keystore), and reloads it on every
// launch — the same key is its stable mobile-verifier identity across the
// registry's committed-index lifetime.
//
// All functions take/return hex strings so they cross the gomobile boundary
// cleanly (no structs, maps, or byte slices in signatures). The secret hex is
// the ONLY thing the app must keep secret and persist; pubkey, address and PoP
// are all derivable from it.

package evmsdk

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateMobileBLSKey creates a fresh BLS identity for this device. It
// returns the 32-byte secret hex (persist this in secure storage), the
// 48-byte compressed public key hex (the registry identity), and the 20-byte
// address hex (first 20 bytes of the pubkey — a display/identity convenience,
// matching the validator address convention). Call once, then persist privHex.
//
// privHex is a 32-byte SEED interpreted exactly as NewMobileVerifyClient /
// decodeSecretKey interpret it (SecretKeyFromRandom32Byte KDF), so a key
// generated here reloads to the identical BLS key — the round-trip the
// lifecycle test pins.
func GenerateMobileBLSKey() (privHex, pubHex, addrHex string, err error) {
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return "", "", "", fmt.Errorf("mobileverify keygen: %w", err)
	}
	privHex = hex.EncodeToString(seed[:])
	sk, err := decodeSecretKey(privHex)
	if err != nil {
		return "", "", "", fmt.Errorf("mobileverify keygen: %w", err)
	}
	pub := sk.PublicKey().Marshal()
	return privHex, hex.EncodeToString(pub), hex.EncodeToString(pub[:20]), nil
}

// MobileBLSPublicKey derives the 48-byte compressed public key hex from a
// persisted secret key hex — the registry identity to register/attest under.
func MobileBLSPublicKey(privHex string) (string, error) {
	sk, err := decodeSecretKey(privHex)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sk.PublicKey().Marshal()), nil
}

// MobileBLSAddress derives the 20-byte address hex (first 20 bytes of the
// pubkey) from a persisted secret key hex.
func MobileBLSAddress(privHex string) (string, error) {
	pub, err := MobileBLSPublicKey(privHex)
	if err != nil {
		return "", err
	}
	if len(pub) < 40 {
		return "", fmt.Errorf("mobileverify: short pubkey")
	}
	return pub[:40], nil
}

// MobileBLSProofOfPossession produces the registration proof-of-possession
// signature hex — the BLS signature over PoPSigningMessage(pubkey) that the
// register endpoint verifies (defends the aggregate against rogue-key
// attacks). MobileVerifyClient.Register produces this internally; this is
// exposed for apps that manage the registration HTTP call themselves.
func MobileBLSProofOfPossession(privHex string) (string, error) {
	sk, err := decodeSecretKey(privHex)
	if err != nil {
		return "", err
	}
	var pubkey [48]byte
	copy(pubkey[:], sk.PublicKey().Marshal())
	sig := sk.Sign(PoPSigningMessage(pubkey))
	return hex.EncodeToString(sig.Marshal()), nil
}
