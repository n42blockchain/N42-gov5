// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// fixedLookup knows a fixed (hash -> number) set.
func fixedLookup(known map[types.Hash]uint64) HeaderLookup {
	return func(h types.Hash) (uint64, bool) {
		n, ok := known[h]
		return n, ok
	}
}

func TestWindowManagerLifecycle(t *testing.T) {
	reg := NewRegistry()
	d := newDevice(t)
	if _, err := reg.Register(d.pubkey, d.pop()); err != nil {
		t.Fatal(err)
	}
	blockHash := h(0x77)
	store := NewCertStore(8)
	m := NewWindowManager(reg, fixedLookup(map[types.Hash]uint64{blockHash: 99}), 50*time.Millisecond, store)

	// Unknown block: rejected, no window opened.
	if _, err := m.Submit(d.receipt(h(0x01), 1, h(0x02))); err == nil {
		t.Fatal("receipt for unknown block accepted")
	}
	// Number mismatch vs the local header: rejected.
	if _, err := m.Submit(d.receipt(blockHash, 100, h(0x02))); err == nil {
		t.Fatal("receipt with wrong number accepted")
	}
	// Valid: opens the window.
	if _, err := m.Submit(d.receipt(blockHash, 99, h(0x02))); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	if m.OpenWindows() != 1 {
		t.Fatalf("open windows = %d, want 1", m.OpenWindows())
	}

	// Window closes on its own and the cert lands in the store.
	deadline := time.Now().Add(2 * time.Second)
	for store.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	certs := store.Get(blockHash)
	if len(certs) != 1 {
		t.Fatalf("certs = %d, want 1", len(certs))
	}
	if _, err := certs[0].Verify(reg); err != nil {
		t.Fatalf("stored cert does not verify: %v", err)
	}
	if m.OpenWindows() != 0 {
		t.Fatalf("open windows after close = %d, want 0", m.OpenWindows())
	}

	// Stop is safe and further submits are rejected.
	m.Stop()
	if _, err := m.Submit(d.receipt(blockHash, 99, h(0x02))); err == nil {
		t.Fatal("submit after stop accepted")
	}
}

func TestCertStoreEviction(t *testing.T) {
	s := NewCertStore(2)
	mk := func(b byte) []*MobileAttestationCert {
		return []*MobileAttestationCert{{BlockHash: h(b), BlockNumber: uint64(b)}}
	}
	s.Put(mk(1))
	s.Put(mk(2))
	s.Put(mk(3)) // evicts block 1
	if s.Get(h(1)) != nil {
		t.Fatal("oldest block not evicted")
	}
	if s.Get(h(2)) == nil || s.Get(h(3)) == nil {
		t.Fatal("recent blocks missing")
	}
}

// TestHTTPServerEndToEnd drives the full phone-facing loop over real
// HTTP: register -> fetch packet -> submit receipt -> read certificate.
func TestHTTPServerEndToEnd(t *testing.T) {
	reg := NewRegistry()
	cache := NewPacketCache(8)
	packets := NewPacketService(cache, nil, "/test")

	// A real packet in the cache, and a lookup that knows its block.
	blockHash, encoded := buildTestPacket(t, 42)
	cache.Put(blockHash, 42, encoded)
	store := NewCertStore(8)
	windows := NewWindowManager(reg, fixedLookup(map[types.Hash]uint64{blockHash: 42}), 60*time.Millisecond, store)
	defer windows.Stop()

	srv := NewHTTPServer("127.0.0.1:0", reg, packets, windows, store)
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	// 1. Register.
	d := newDevice(t)
	pop := d.pop()
	regBody, _ := json.Marshal(registerRequest{
		Pubkey: hex.EncodeToString(d.pubkey[:]),
		PoP:    hex.EncodeToString(pop[:]),
	})
	resp, err := http.Post(ts.URL+"/mobileverify/register", "application/json", bytes.NewReader(regBody))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("register: %v status=%v", err, resp.Status)
	}
	resp.Body.Close()

	// Bad PoP is refused.
	badBody, _ := json.Marshal(registerRequest{
		Pubkey: hex.EncodeToString(d.pubkey[:]),
		PoP:    hex.EncodeToString(make([]byte, 96)),
	})
	resp, _ = http.Post(ts.URL+"/mobileverify/register", "application/json", bytes.NewReader(badBody))
	if resp.StatusCode == 200 {
		t.Fatal("garbage PoP registered")
	}
	resp.Body.Close()

	// 2. Fetch the packet.
	resp, err = http.Get(ts.URL + "/mobileverify/packet/" + blockHash.Hex())
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("packet fetch: %v status=%v", err, resp.Status)
	}
	resp.Body.Close()
	resp, _ = http.Get(ts.URL + "/mobileverify/packet/" + h(0xEE).Hex())
	if resp.StatusCode != 404 {
		t.Fatalf("missing packet status = %v, want 404", resp.Status)
	}
	resp.Body.Close()

	// 3. Submit a receipt (signed exactly as the SDK would).
	root := h(0x0C)
	rcpt := d.receipt(blockHash, 42, root)
	rcptBody, _ := json.Marshal(receiptRequest{
		BlockHash:    blockHash.Hex(),
		BlockNumber:  42,
		ReceiptsRoot: root.Hex(),
		Pubkey:       hex.EncodeToString(rcpt.VerifierPubkey[:]),
		Signature:    hex.EncodeToString(rcpt.Signature[:]),
	})
	resp, err = http.Post(ts.URL+"/mobileverify/receipt", "application/json", bytes.NewReader(rcptBody))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("receipt: %v status=%v", err, resp.Status)
	}
	resp.Body.Close()

	// Unregistered signer refused.
	stranger := newDevice(t)
	sRcpt := stranger.receipt(blockHash, 42, root)
	sBody, _ := json.Marshal(receiptRequest{
		BlockHash: blockHash.Hex(), BlockNumber: 42, ReceiptsRoot: root.Hex(),
		Pubkey:    hex.EncodeToString(sRcpt.VerifierPubkey[:]),
		Signature: hex.EncodeToString(sRcpt.Signature[:]),
	})
	resp, _ = http.Post(ts.URL+"/mobileverify/receipt", "application/json", bytes.NewReader(sBody))
	if resp.StatusCode == 200 {
		t.Fatal("unregistered receipt accepted over HTTP")
	}
	resp.Body.Close()

	// 4. Wait for the window to close, then read the certificate.
	deadline := time.Now().Add(2 * time.Second)
	for store.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	resp, err = http.Get(ts.URL + "/mobileverify/cert/" + blockHash.Hex())
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("cert fetch: %v status=%v", err, resp.Status)
	}
	var certs []certJSON
	if err := json.NewDecoder(resp.Body).Decode(&certs); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(certs) != 1 || certs[0].Signers != 1 || certs[0].BlockNumber != 42 {
		t.Fatalf("cert JSON wrong: %+v", certs)
	}

	// 5. Health reflects the pipeline.
	resp, _ = http.Get(ts.URL + "/mobileverify/health")
	var health map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if fmt.Sprint(health["registry"]) != "1" {
		t.Fatalf("health registry = %v, want 1", health["registry"])
	}
}

func TestHTTPRegisterRateLimit(t *testing.T) {
	reg := NewRegistry()
	store := NewCertStore(2)
	windows := NewWindowManager(reg, fixedLookup(nil), time.Second, store)
	defer windows.Stop()
	srv := NewHTTPServer("127.0.0.1:0", reg, NewPacketService(NewPacketCache(2), nil, "/t"), windows, store)
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	// The limiter allows 10/min/IP; the 11th must get 429.
	last := 0
	for i := 0; i < 11; i++ {
		d := newDevice(t)
		pop := d.pop()
		body, _ := json.Marshal(registerRequest{
			Pubkey: hex.EncodeToString(d.pubkey[:]),
			PoP:    hex.EncodeToString(pop[:]),
		})
		resp, err := http.Post(ts.URL+"/mobileverify/register", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		last = resp.StatusCode
		resp.Body.Close()
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("11th registration status = %d, want 429", last)
	}
}
