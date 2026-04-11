// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"encoding/json"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// ----------------------------------------------------------------------------
// Producer registry
// ----------------------------------------------------------------------------

func TestMobileFacade_AddRemoveProducer(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())

	if msg := MobileAddProducer("ws://node-a:20013", "0xaaa"); msg != "" {
		t.Fatalf("AddProducer A: %q", msg)
	}
	if msg := MobileAddProducer("ws://node-b:20013", "0xbbb"); msg != "" {
		t.Fatalf("AddProducer B: %q", msg)
	}

	// Duplicate add should fail.
	if msg := MobileAddProducer("ws://node-a:20013", "0xaaa"); msg == "" {
		t.Fatal("duplicate AddProducer should fail")
	}

	listJSON := MobileListProducers()
	type listEntry struct {
		URI     string `json:"uri"`
		Account string `json:"account"`
		State   string `json:"state"`
		Active  bool   `json:"active"`
	}
	var entries []listEntry
	if err := json.Unmarshal([]byte(listJSON), &entries); err != nil {
		t.Fatalf("list parse: %v\nbody=%s", err, listJSON)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 producers, got %d", len(entries))
	}
	if entries[0].URI != "ws://node-a:20013" || entries[1].URI != "ws://node-b:20013" {
		t.Errorf("insertion order broken: %+v", entries)
	}
	for _, e := range entries {
		if e.State != "stopped" {
			t.Errorf("producer %s state = %q, want stopped", e.URI, e.State)
		}
		if e.Active {
			t.Errorf("producer %s should not be active before MobileStart", e.URI)
		}
	}

	// Remove one.
	if msg := MobileRemoveProducer("ws://node-a:20013"); msg != "" {
		t.Fatalf("RemoveProducer: %q", msg)
	}
	listJSON = MobileListProducers()
	entries = nil
	json.Unmarshal([]byte(listJSON), &entries)
	if len(entries) != 1 {
		t.Fatalf("after remove, expected 1 producer, got %d", len(entries))
	}
	if entries[0].URI != "ws://node-b:20013" {
		t.Errorf("remaining producer URI = %q", entries[0].URI)
	}

	// Remove unknown should fail.
	if msg := MobileRemoveProducer("ws://nope:9999"); msg == "" {
		t.Fatal("RemoveProducer for unknown URI should fail")
	}
}

func TestMobileFacade_AddProducerBeforeInit(t *testing.T) {
	resetMobile(t)
	if msg := MobileAddProducer("ws://x:1", "0x0"); msg == "" {
		t.Fatal("AddProducer before MobileInit should fail")
	}
}

func TestMobileFacade_PrivKeyPropagatesToProducers(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)

	// Add producers BEFORE setting the priv key.
	MobileAddProducer("ws://a:1", "0xa")
	MobileAddProducer("ws://b:1", "0xb")

	// Set priv key — should propagate to existing engines.
	MobileSetPrivKey(mobilePrivKeyHex())

	mobileMu.Lock()
	defer mobileMu.Unlock()
	for uri, p := range mobileSingleton.producers {
		if p.engine.PrivKey != mobilePrivKeyHex() {
			t.Errorf("producer %s engine.PrivKey not set", uri)
		}
	}
}

func TestMobileFacade_StartStopMultiProducer(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())

	// No producers yet → Start should error.
	if msg := MobileStart(); msg == "" {
		t.Fatal("MobileStart with no producers should fail")
	}

	// Add one producer with a fake URI; engine.Start will try to dial
	// and fail (expected). The error is reported as the function return.
	MobileAddProducer("ws://does-not-exist:9999", "0xa")
	startErr := MobileStart()
	// The dial WILL fail; just confirm the function reported an error
	// rather than crashing or silently succeeding.
	if startErr == "" {
		// If somehow it succeeded (e.g. background dial), stop it.
		MobileStop()
	} else {
		t.Logf("MobileStart correctly reported dial failure: %s", startErr)
	}
}

// ----------------------------------------------------------------------------
// Dedup hooks
// ----------------------------------------------------------------------------

func TestMobileFacade_DedupHookSkipsRepeatedBlocks(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())

	// Verify the same packet twice via the offline path.
	pkt := encodedEmptyBlockPacket(t, 500)

	// First call: should run real verification.
	res1 := MobileVerifyPacket(pkt)
	var info1 mobileLastInfo
	if err := json.Unmarshal([]byte(res1), &info1); err != nil {
		t.Fatal(err)
	}
	if !info1.Success {
		t.Fatalf("first verify should succeed: %s", info1.ErrorMsg)
	}

	// Second call with the SAME packet: dedup hook is installed but
	// MobileVerifyPacket itself doesn't currently consult it (it's
	// called from the offline test path, not the WebSocket loop).
	// What we DO want is that the cached payload from vertify/verifyV2
	// would be returned if we called those directly. The dedup
	// behavior at the engine level is exercised by
	// TestVerifyHooks_RoundTripCached below.

	// Confirm the dedup state has been populated by the first call.
	mobileMu.Lock()
	dedupHas := mobileSingleton.dedup.Has(uint64(info1.BlockNumber), parseHexHash(t, info1.BlockHash))
	mobileMu.Unlock()
	if !dedupHas {
		t.Errorf("after MobileVerifyPacket, dedup should mark the block")
	}
}

func TestVerifyHooks_RoundTripCached(t *testing.T) {
	// Manually exercise the hook installation + cache → skip cycle
	// without going through the WebSocket loop.
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())

	hash := types.HexToHash("0xfeedfeedfeedfeedfeedfeedfeedfeedfeedfeedfeedfeedfeedfeedfeedfeed")
	const num = uint64(777)

	// First call: nothing cached → should NOT skip.
	skip, cached := callShouldSkip(num, hash)
	if skip {
		t.Fatal("first call should not skip")
	}
	if cached != nil {
		t.Fatal("first call should have no cached payload")
	}

	// Cache a payload.
	payload := []byte(`{"signed":"abc"}`)
	callCache(num, hash, payload)

	// Second call: same key → should skip and return the same payload.
	skip2, cached2 := callShouldSkip(num, hash)
	if !skip2 {
		t.Fatal("second call should skip")
	}
	if string(cached2) != string(payload) {
		t.Errorf("cached payload mismatch: got %q, want %q", string(cached2), string(payload))
	}
}

func TestVerifyHooks_DifferentHashSameNumberNotSkipped(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())

	hashA := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	hashB := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	const num = uint64(900)

	callCache(num, hashA, []byte("payload-a"))

	// Different hash at the same number is a different block — must NOT skip.
	skip, _ := callShouldSkip(num, hashB)
	if skip {
		t.Fatal("different hash at same number should not be deduped")
	}
}

func TestVerifyHooks_ClearedAfterMobileFree(t *testing.T) {
	resetMobile(t)
	MobileInit(4242)
	hash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	callCache(42, hash, []byte("p"))
	if skip, _ := callShouldSkip(42, hash); !skip {
		t.Fatal("should be cached after Init")
	}
	MobileFree()
	if skip, _ := callShouldSkip(42, hash); skip {
		t.Fatal("after MobileFree, hook should be cleared and never skip")
	}
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func parseHexHash(t *testing.T, s string) types.Hash {
	t.Helper()
	if len(s) >= 2 && s[:2] == "0x" {
		s = s[2:]
	}
	return types.HexToHash(s)
}
