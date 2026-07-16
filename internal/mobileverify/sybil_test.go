// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/crypto/bls"
)

func TestPoWSolveVerifyRoundTrip(t *testing.T) {
	var pubkey [48]byte
	pubkey[0], pubkey[47] = 0xAB, 0xCD

	const bits = 12 // cheap for CI, same code path as production difficulty
	nonce, ok := SolvePoW(pubkey, bits, 0)
	if !ok {
		t.Fatal("solver ran out of budget at 12 bits")
	}
	if !VerifyPoW(pubkey, nonce, bits) {
		t.Fatal("solved nonce does not verify")
	}
	// A different key must not accept the same nonce (message is key-bound)
	// unless it happens to solve too — overwhelmingly unlikely at 12 bits.
	var other [48]byte
	other[0] = 0xEE
	if VerifyPoW(other, nonce, bits) {
		t.Fatal("nonce for one key verified for another — message not key-bound?")
	}
	// Disabled difficulty always verifies.
	if !VerifyPoW(pubkey, 0, 0) {
		t.Fatal("bits=0 must always verify")
	}
}

// TestPoWMessageMatchesSDK pins the server and evmsdk PoW message bytes to
// each other — the tag is restated in the SDK (import cycle), so this test is
// the only thing holding the two together.
func TestPoWMessageMatchesSDK(t *testing.T) {
	var pubkey [48]byte
	for i := range pubkey {
		pubkey[i] = byte(i * 5)
	}
	for _, nonce := range []uint64{0, 1, 0xDEADBEEF, ^uint64(0)} {
		if !bytes.Equal(PoWMessage(pubkey, nonce), evmsdk.PoWMessage(pubkey, nonce)) {
			t.Fatalf("PoW message diverged from SDK at nonce %d", nonce)
		}
	}
}

type rejectAllAttestor struct{}

func (rejectAllAttestor) VerifyDevice(pubkey [48]byte, attestation []byte) error {
	return errors.New("no device attestation scheme accepts this")
}

type acceptAttestor struct{ want []byte }

func (a acceptAttestor) VerifyDevice(pubkey [48]byte, attestation []byte) error {
	if !bytes.Equal(attestation, a.want) {
		return errors.New("wrong blob")
	}
	return nil
}

// TestRegisterPoWGateEndToEnd runs the real SDK client against the real HTTP
// server with the PoW gate armed: the client's first attempt gets 428, it
// solves the puzzle, retries, and registers. Then the attestation gate is
// armed and shown to reject.
func TestRegisterPoWGateEndToEnd(t *testing.T) {
	reg := NewRegistry()
	srv := NewHTTPServer("127.0.0.1:0", reg, nil, nil, nil)
	srv.SetRegisterPoWBits(10) // cheap but non-trivial
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	client, err := evmsdk.NewMobileVerifyClient(ts.URL, skToHex(t, sk.Marshal()))
	if err != nil {
		t.Fatal(err)
	}
	// The SDK's first attempt gets HTTP 428, solves the PoW, and retries.
	if _, err := client.Register(context.Background()); err != nil {
		t.Fatalf("register through PoW gate failed: %v", err)
	}
	if reg.PendingCount()+reg.Count() == 0 {
		t.Fatal("registration did not land in the registry")
	}

	// Arm the attestation gate: a client that sends no attestation is refused.
	srv.SetDeviceAttestor(rejectAllAttestor{})
	sk2, _ := bls.RandKey()
	client2, _ := evmsdk.NewMobileVerifyClient(ts.URL, skToHex(t, sk2.Marshal()))
	if _, err := client2.Register(context.Background()); err == nil {
		t.Fatal("attestation gate let an unattested registration through")
	}
}

var _ = acceptAttestor{} // accept-path attestor kept for wiring reference
