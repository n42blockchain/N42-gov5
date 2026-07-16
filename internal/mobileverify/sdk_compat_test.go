// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Pins this package's signing message byte-exactly to the production
// mobile SDK's (cmd/evmsdk/verification_receipt.go, itself pinned to
// the Rust reference). If either side drifts, receipts signed by real
// phones stop aggregating here — this test makes that a compile-time
// visible, test-time loud event instead of a silent field failure.

package mobileverify

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

func TestSigningMessageMatchesSDK(t *testing.T) {
	var blockHash, root types.Hash
	for i := range blockHash {
		blockHash[i] = byte(i)
		root[i] = byte(0xFF - i)
	}
	const number = uint64(13_220_145)

	ours := SigningMessage(blockHash, number, root)
	sdks := evmsdk.BuildSigningMessage(blockHash, number, root)
	if !bytes.Equal(ours, sdks) {
		t.Fatalf("signing message diverged from the SDK:\n ours: %x\n sdk:  %x", ours, sdks)
	}
	if len(ours) != SigningMessageLen {
		t.Fatalf("len = %d, want %d", len(ours), SigningMessageLen)
	}
}

// TestSDKReceiptVerifiesHere proves a receipt built and signed exactly
// the way the SDK does it is accepted by this package's collector —
// the unification contract in one test.
func TestSDKReceiptVerifiesHere(t *testing.T) {
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	var pubkey [48]byte
	copy(pubkey[:], sk.PublicKey().Marshal())

	var blockHash, root types.Hash
	blockHash[0], root[0] = 0xA1, 0xB2
	const number = uint64(777)

	// Sign the SDK's message, not ours.
	sdkReceipt := evmsdk.VerificationReceipt{
		BlockHash:            blockHash,
		BlockNumber:          number,
		ComputedReceiptsRoot: root,
		VerifierPubkey:       pubkey,
	}
	copy(sdkReceipt.Signature[:], sk.Sign(sdkReceipt.SigningMessage()).Marshal())
	if err := sdkReceipt.VerifySignature(); err != nil {
		t.Fatalf("sdk-side verify: %v", err)
	}

	// Field-for-field into the server-side type; it must verify and
	// be accepted by a collector after registration.
	reg := NewRegistry()
	var pop [96]byte
	copy(pop[:], sk.Sign(PoPMessage(pubkey)).Marshal())
	if _, err := reg.Register(pubkey, pop); err != nil {
		t.Fatalf("register: %v", err)
	}
	serverReceipt := &Receipt{
		BlockHash:            sdkReceipt.BlockHash,
		BlockNumber:          sdkReceipt.BlockNumber,
		ComputedReceiptsRoot: sdkReceipt.ComputedReceiptsRoot,
		VerifierPubkey:       sdkReceipt.VerifierPubkey,
		Signature:            sdkReceipt.Signature,
		TimestampMs:          sdkReceipt.TimestampMs,
	}
	col := NewCollector(reg, blockHash, number)
	if _, err := col.Add(serverReceipt); err != nil {
		t.Fatalf("collector rejected an SDK-signed receipt: %v", err)
	}
	certs, err := col.Close(1)
	if err != nil || len(certs) != 1 {
		t.Fatalf("close: %v, certs=%d", err, len(certs))
	}
	if _, err := certs[0].Verify(reg); err != nil {
		t.Fatalf("cert from SDK-signed receipt failed verification: %v", err)
	}
}
