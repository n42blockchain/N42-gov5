// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"encoding/hex"
	"testing"

	"github.com/n42blockchain/N42/crypto/bls"
)

// TestMobileIdentityLifecycle: generate → persist(hex) → reload derives the
// SAME pubkey/address, and the PoP verifies against the derived pubkey.
func TestMobileIdentityLifecycle(t *testing.T) {
	privHex, pubHex, addrHex, err := GenerateMobileBLSKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(mustHex(t, privHex)) != 32 || len(mustHex(t, pubHex)) != 48 || len(mustHex(t, addrHex)) != 20 {
		t.Fatalf("bad lengths: priv=%d pub=%d addr=%d", len(privHex)/2, len(pubHex)/2, len(addrHex)/2)
	}

	// Reload from persisted secret — derivations must be stable.
	gotPub, err := MobileBLSPublicKey(privHex)
	if err != nil || gotPub != pubHex {
		t.Fatalf("pubkey not stable across reload: %v (%s vs %s)", err, gotPub, pubHex)
	}
	gotAddr, err := MobileBLSAddress(privHex)
	if err != nil || gotAddr != addrHex {
		t.Fatalf("address not stable: %v", err)
	}

	// PoP must verify against the pubkey (rogue-key defence at registration).
	popHex, err := MobileBLSProofOfPossession(privHex)
	if err != nil {
		t.Fatal(err)
	}
	var pubkey [48]byte
	copy(pubkey[:], mustHex(t, pubHex))
	sig, err := bls.SignatureFromBytes(mustHex(t, popHex))
	if err != nil {
		t.Fatal(err)
	}
	pk, err := bls.PublicKeyFromBytes(pubkey[:])
	if err != nil {
		t.Fatal(err)
	}
	if !sig.Verify(pk, PoPSigningMessage(pubkey)) {
		t.Fatal("PoP signature does not verify against the derived pubkey")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
