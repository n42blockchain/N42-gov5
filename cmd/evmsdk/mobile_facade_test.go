// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// resetMobile clears any singleton state from prior tests so that
// MobileInit / MobileFree pairs are isolated. Tests that touch the
// singleton must call this in defer.
func resetMobile(t *testing.T) {
	t.Helper()
	mobileMu.Lock()
	mobileSingleton = nil
	mobileMu.Unlock()
}

// mobilePrivKeyHex returns a deterministic 32-byte BLS-compatible secret
// in hex form. Avoids randomness so failures are reproducible.
func mobilePrivKeyHex() string {
	var sk [32]byte
	for i := range sk {
		sk[i] = byte(i + 1)
	}
	return hex.EncodeToString(sk[:])
}

// ----------------------------------------------------------------------------
// Lifecycle
// ----------------------------------------------------------------------------

func TestMobileFacade_LifecycleWithoutInit(t *testing.T) {
	resetMobile(t)
	defer MobileFree()

	if got := MobileGetPubkey(); got != "" {
		t.Errorf("pubkey before init = %q, want empty", got)
	}
	if got := MobileState(); got != "uninitialized" {
		t.Errorf("state before init = %q, want uninitialized", got)
	}
	if got := MobileGetLastVerifyInfo(); got != "{}" {
		t.Errorf("last info before init = %q, want {}", got)
	}
	if got := MobileSetServer("ws://x", "0xabc"); !strings.Contains(got, "not initialized") {
		t.Errorf("SetServer before init = %q, want 'not initialized'", got)
	}
}

func TestMobileFacade_InitSetPrivKey(t *testing.T) {
	resetMobile(t)
	defer MobileFree()

	if msg := MobileInit(4242); msg != "" {
		t.Fatalf("MobileInit = %q, want empty", msg)
	}
	if msg := MobileSetPrivKey(mobilePrivKeyHex()); msg != "" {
		t.Fatalf("MobileSetPrivKey = %q, want empty", msg)
	}
	pub := MobileGetPubkey()
	if len(pub) != 96 { // 48-byte BLS pubkey hex
		t.Fatalf("pubkey hex len = %d, want 96", len(pub))
	}

	// Re-init wipes state.
	if msg := MobileInit(4242); msg != "" {
		t.Fatalf("re-init = %q", msg)
	}
	if got := MobileGetPubkey(); got != "" {
		t.Errorf("pubkey after re-init = %q, want empty", got)
	}
}

func TestMobileFacade_SetPrivKeyInvalid(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)

	if msg := MobileSetPrivKey("not-hex"); msg == "" {
		t.Fatal("expected error from non-hex private key")
	}
	if msg := MobileSetPrivKey(""); msg == "" {
		t.Fatal("expected error from empty private key")
	}
	if got := MobileGetPubkey(); got != "" {
		t.Errorf("pubkey should remain empty after failed SetPrivKey, got %q", got)
	}
}

func TestMobileFacade_SetServer(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)

	if msg := MobileSetServer("ws://localhost:20013", "0xdeadbeef"); msg != "" {
		t.Fatalf("MobileSetServer = %q, want empty", msg)
	}
	mobileMu.Lock()
	defer mobileMu.Unlock()
	p, ok := mobileSingleton.producers["ws://localhost:20013"]
	if !ok {
		t.Fatalf("expected producer entry for ws://localhost:20013, got %v", mobileSingleton.producers)
	}
	if p.engine.ServerUri != "ws://localhost:20013" {
		t.Errorf("engine.ServerUri = %q", p.engine.ServerUri)
	}
	if p.engine.Account != "0xdeadbeef" {
		t.Errorf("engine.Account = %q", p.engine.Account)
	}
}

func TestMobileFacade_State(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	if got := MobileState(); got != "uninitialized" {
		t.Errorf("state = %q, want uninitialized", got)
	}
	MobileInit(4242)
	// engine is fresh, EngineState empty → State() returns "stopped"
	if got := MobileState(); got != "stopped" {
		t.Errorf("state after init = %q, want stopped", got)
	}
}

// ----------------------------------------------------------------------------
// Stats
// ----------------------------------------------------------------------------

func TestMobileFacade_StatsBeforeInit(t *testing.T) {
	resetMobile(t)
	defer MobileFree()

	js := MobileGetStats()
	var stats map[string]int64
	if err := json.Unmarshal([]byte(js), &stats); err != nil {
		t.Fatalf("stats JSON parse: %v", err)
	}
	if stats["blocks_verified"] != 0 || stats["success_count"] != 0 {
		t.Errorf("stats not zeroed: %v", stats)
	}
}

func TestMobileFacade_StatsAfterVerify(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	if msg := MobileSetPrivKey(mobilePrivKeyHex()); msg != "" {
		t.Fatalf("MobileSetPrivKey: %q", msg)
	}

	pkt := encodedEmptyBlockPacket(t, 100)

	res := MobileVerifyPacket(pkt)
	var info mobileLastInfo
	if err := json.Unmarshal([]byte(res), &info); err != nil {
		t.Fatalf("verify result JSON parse: %v\nbody=%s", err, res)
	}
	if !info.Success {
		t.Fatalf("verify should succeed, got error: %s", info.ErrorMsg)
	}
	if info.BlockNumber != 100 {
		t.Errorf("blockNumber = %d, want 100", info.BlockNumber)
	}
	if info.Signature == "0x"+strings.Repeat("00", 96) {
		t.Errorf("signature is all zeros — sign step did not run")
	}

	// Stats reflect 1 successful verification.
	statsJSON := MobileGetStats()
	var stats struct {
		BlocksVerified int64 `json:"blocks_verified"`
		SuccessCount   int64 `json:"success_count"`
		FailureCount   int64 `json:"failure_count"`
		SuccessRate    int64 `json:"success_rate"`
	}
	if err := json.Unmarshal([]byte(statsJSON), &stats); err != nil {
		t.Fatalf("stats JSON parse: %v", err)
	}
	if stats.BlocksVerified != 1 || stats.SuccessCount != 1 || stats.FailureCount != 0 {
		t.Errorf("stats wrong: %+v", stats)
	}
	if stats.SuccessRate != 100 {
		t.Errorf("success rate = %d, want 100", stats.SuccessRate)
	}

	// Last verify info matches.
	lastJSON := MobileGetLastVerifyInfo()
	var last mobileLastInfo
	if err := json.Unmarshal([]byte(lastJSON), &last); err != nil {
		t.Fatalf("last info JSON parse: %v", err)
	}
	if last.BlockNumber != 100 || !last.Success {
		t.Errorf("last verify info wrong: %+v", last)
	}
}

func TestMobileFacade_VerifyFailureAccountedInStats(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())

	// Garbage bytes — decode will fail.
	res := MobileVerifyPacket([]byte{0x00, 0x01, 0x02})
	var info mobileLastInfo
	json.Unmarshal([]byte(res), &info)
	if info.Success {
		t.Fatal("verify should have failed on garbage input")
	}
	if info.ErrorMsg == "" {
		t.Error("expected error message in result")
	}

	statsJSON := MobileGetStats()
	var stats struct {
		BlocksVerified int64 `json:"blocks_verified"`
		FailureCount   int64 `json:"failure_count"`
	}
	json.Unmarshal([]byte(statsJSON), &stats)
	if stats.BlocksVerified != 1 || stats.FailureCount != 1 {
		t.Errorf("failure not accounted: %+v", stats)
	}
}

func TestMobileFacade_VerifyWithoutInit(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	res := MobileVerifyPacket([]byte{0x00})
	if !strings.Contains(res, "not initialized") {
		t.Errorf("expected 'not initialized' in error, got %q", res)
	}
}

// ----------------------------------------------------------------------------
// Free
// ----------------------------------------------------------------------------

func TestMobileFacade_FreeIdempotent(t *testing.T) {
	resetMobile(t)
	MobileInit(4242)
	MobileFree()
	MobileFree() // double free should be safe
	if got := MobileState(); got != "uninitialized" {
		t.Errorf("state after free = %q, want uninitialized", got)
	}
}

// ----------------------------------------------------------------------------
// Version
// ----------------------------------------------------------------------------

func TestMobileFacade_Version(t *testing.T) {
	v := MobileVersion()
	if v == "" {
		t.Fatal("MobileVersion empty")
	}
	if !strings.Contains(v, "v1") {
		t.Errorf("expected version string to contain 'v1', got %q", v)
	}
}

// ----------------------------------------------------------------------------
// Test helpers
// ----------------------------------------------------------------------------

// encodedEmptyBlockPacket builds a wire-encoded empty-block StreamPacket
// suitable for end-to-end MobileVerifyPacket testing.
func encodedEmptyBlockPacket(t *testing.T, blockNumber uint64) []byte {
	t.Helper()
	hdr := minimalHeader(t, blockNumber, emptyEthTrieRoot)
	pkt := emptyPacket(t, hdr)
	encoded, err := EncodeStreamPacket(pkt)
	if err != nil {
		t.Fatalf("encode stream packet: %v", err)
	}
	return encoded
}
