// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase-4 unification proof: the ACTUAL SDK client (cmd/evmsdk's
// MobileVerifyClient — the code the embedded SDK and the standalone app
// both converge on) drives the ACTUAL phase-3 server over real HTTP:
// register with PoP -> fetch a packet -> submit a signed receipt ->
// the window closes into a certificate that verifies against the
// registry. Plus the byte-pin between the SDK's restated PoP message
// and this package's canonical one.

package mobileverify

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

func TestPoPMessageMatchesSDK(t *testing.T) {
	var pubkey [48]byte
	for i := range pubkey {
		pubkey[i] = byte(i * 3)
	}
	if !bytes.Equal(PoPMessage(pubkey), evmsdk.PoPSigningMessage(pubkey)) {
		t.Fatalf("PoP message diverged from the SDK:\n ours: %x\n sdk:  %x",
			PoPMessage(pubkey), evmsdk.PoPSigningMessage(pubkey))
	}
}

func TestSDKClientEndToEnd(t *testing.T) {
	// Server side: the real phase-3 pipeline.
	reg := NewRegistry()
	cache := NewPacketCache(8)
	packets := NewPacketService(cache, nil, "/test")
	blockHash, encoded := buildTestPacket(t, 88)
	cache.Put(blockHash, 88, encoded)
	store := NewCertStore(8)
	windows := NewWindowManager(reg, fixedLookup(map[types.Hash]uint64{blockHash: 88}), 60*time.Millisecond, store)
	defer windows.Stop()
	srv := NewHTTPServer("127.0.0.1:0", reg, packets, windows, store)
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	// Client side: the real SDK client with a real BLS identity.
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	client, err := evmsdk.NewMobileVerifyClient(ts.URL, skToHex(t, sk.Marshal()))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	ctx := context.Background()

	// 1. Register — lands in pending, idempotent re-register (app relaunch shape).
	if _, err := client.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := client.Register(ctx); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if reg.PendingCount() != 1 {
		t.Fatalf("pending count = %d, want 1", reg.PendingCount())
	}
	// Commit the epoch so the device is a committed member and can attest.
	reg.CommitEpoch()
	idx, ok := reg.Lookup(client.Pubkey())
	if !ok {
		t.Fatal("device not committed after CommitEpoch")
	}
	if reg.Count() != 1 {
		t.Fatalf("registry count = %d, want 1", reg.Count())
	}

	// 2. Fetch the packet through the client codec path.
	pkt, err := client.FetchPacket(ctx, blockHash.Hex())
	if err != nil {
		t.Fatalf("fetch packet: %v", err)
	}
	if pkt.BlockHash != blockHash {
		t.Fatalf("packet hash = %x, want %x", pkt.BlockHash[:4], blockHash[:4])
	}

	// 3. Sign and submit a receipt exactly as VerifyBlock would after a
	// successful re-execution (the execution itself has its own suite —
	// this test pins the transport + attestation contract).
	root := h(0x66)
	receipt := client.SignReceipt(&evmsdk.V2VerifyResult{
		BlockHash:            blockHash,
		BlockNumber:          88,
		ComputedReceiptsRoot: root,
	})
	if err := receipt.VerifySignature(); err != nil {
		t.Fatalf("SDK-signed receipt invalid: %v", err)
	}
	if _, err := client.SubmitReceipt(ctx, receipt); err != nil {
		t.Fatalf("submit receipt: %v", err)
	}

	// 4. The window closes into a certificate that verifies.
	deadline := time.Now().Add(2 * time.Second)
	for store.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	certs := store.Get(blockHash)
	if len(certs) != 1 {
		t.Fatalf("certs = %d, want 1", len(certs))
	}
	cohort, err := certs[0].Verify(reg)
	if err != nil {
		t.Fatalf("cert verify: %v", err)
	}
	if len(cohort) != 1 || cohort[0] != idx {
		t.Fatalf("cohort = %v, want [%d]", cohort, idx)
	}
	if certs[0].ReceiptsRoot != root {
		t.Fatalf("cert root = %x, want %x", certs[0].ReceiptsRoot[:4], root[:4])
	}
}

func skToHex(t *testing.T, b []byte) string {
	t.Helper()
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, hexdigits[x>>4], hexdigits[x&0xF])
	}
	return string(out)
}
