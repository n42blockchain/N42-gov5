// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the single-active producer + rotation monitor.

package evmsdk

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// ----------------------------------------------------------------------------
// Policy
// ----------------------------------------------------------------------------

func TestRotationPolicy_DefaultsAndOverride(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)

	js := MobileGetRotationPolicy()
	var got map[string]int64
	if err := json.Unmarshal([]byte(js), &got); err != nil {
		t.Fatalf("policy parse: %v", err)
	}
	if got["block_time_ms"] != defaultRotationPolicy.BlockTimeMs {
		t.Errorf("default block_time_ms = %d, want %d", got["block_time_ms"], defaultRotationPolicy.BlockTimeMs)
	}
	if got["slow_leader_timeout_ms"] != defaultRotationPolicy.SlowLeaderTimeoutMs {
		t.Errorf("default slow_leader_timeout_ms = %d", got["slow_leader_timeout_ms"])
	}

	// Override.
	if msg := MobileSetRotationPolicy(2000, 5000, 14000, 250); msg != "" {
		t.Fatalf("SetRotationPolicy: %q", msg)
	}
	js = MobileGetRotationPolicy()
	json.Unmarshal([]byte(js), &got)
	if got["block_time_ms"] != 2000 {
		t.Errorf("override block_time_ms = %d, want 2000", got["block_time_ms"])
	}
	if got["slow_leader_timeout_ms"] != 5000 {
		t.Errorf("override slow_leader_timeout_ms = %d, want 5000", got["slow_leader_timeout_ms"])
	}
	if got["rotation_cycle_ms"] != 14000 {
		t.Errorf("override rotation_cycle_ms = %d, want 14000", got["rotation_cycle_ms"])
	}
	if got["monitor_period_ms"] != 250 {
		t.Errorf("override monitor_period_ms = %d, want 250", got["monitor_period_ms"])
	}

	// Zero arg = keep current.
	MobileSetRotationPolicy(0, 999, 0, 0)
	js = MobileGetRotationPolicy()
	json.Unmarshal([]byte(js), &got)
	if got["block_time_ms"] != 2000 {
		t.Errorf("after partial set, block_time_ms changed: %d", got["block_time_ms"])
	}
	if got["slow_leader_timeout_ms"] != 999 {
		t.Errorf("after partial set, slow_leader_timeout_ms = %d, want 999", got["slow_leader_timeout_ms"])
	}
}

func TestRotationPolicy_BeforeInit(t *testing.T) {
	resetMobile(t)
	if msg := MobileSetRotationPolicy(1000, 1000, 1000, 1000); msg == "" {
		t.Fatal("SetRotationPolicy before MobileInit should fail")
	}
	if got := MobileGetRotationPolicy(); got != "{}" {
		t.Errorf("GetRotationPolicy before init = %q, want {}", got)
	}
}

// ----------------------------------------------------------------------------
// Active producer model
// ----------------------------------------------------------------------------

func TestActiveProducer_NoneWhenStopped(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())
	MobileAddProducer("ws://a:1", "0xa")
	MobileAddProducer("ws://b:1", "0xb")

	if got := MobileGetActiveProducer(); got != "" {
		t.Errorf("active before Start = %q, want empty", got)
	}
}

func TestActiveProducer_FirstAfterStart(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())

	// Two unreachable producers — Start tries the FIRST one in insertion
	// order. The dial will fail (host doesn't exist), so MobileStart
	// returns an error and no active is set.
	MobileAddProducer("ws://does-not-exist-a:9999", "0xa")
	MobileAddProducer("ws://does-not-exist-b:9999", "0xb")

	startErr := MobileStart()
	if startErr == "" {
		t.Fatal("MobileStart should have failed (unreachable hosts)")
		MobileStop()
	}
	// activeURI was set to the first URI before the dial; activate fails
	// the engine.Start, returns error, but activateLocked left activeURI
	// untouched on failure. Confirm it's empty.
	if got := MobileGetActiveProducer(); got != "" {
		t.Errorf("after failed Start, active = %q, want empty", got)
	}
}

func TestActiveProducer_StartFailsWithoutCandidates(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())

	if msg := MobileStart(); msg == "" {
		t.Fatal("MobileStart with zero candidates should fail")
	}
}

// ----------------------------------------------------------------------------
// Rotation logic
// ----------------------------------------------------------------------------

// TestRotationTick_HealthyNoRotation: lastBlock recent, monitor must
// NOT rotate.
func TestRotationTick_HealthyNoRotation(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())
	MobileSetRotationPolicy(1000, 3000, 14000, 100)
	MobileAddProducer("ws://a:1", "0xa")
	MobileAddProducer("ws://b:1", "0xb")

	// Pretend the active producer just received a block 100ms ago and
	// activeURI is "ws://a:1".
	mobileMu.Lock()
	mobileSingleton.activeURI = "ws://a:1"
	mobileSingleton.lastBlock.Store(time.Now().UnixMilli() - 100)
	mobileMu.Unlock()

	rotationMonitorTick()

	mobileMu.Lock()
	got := mobileSingleton.activeURI
	mobileMu.Unlock()
	if got != "ws://a:1" {
		t.Errorf("healthy producer was rotated: active = %q", got)
	}
}

// TestRotationTick_SlowFastCycleStays: leader is slow but rotation
// cycle is short → don't rotate.
func TestRotationTick_SlowFastCycleStays(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())
	// slow timeout = 5s, rotation cycle = 3s → cycle ≤ timeout → STAY.
	MobileSetRotationPolicy(1000, 5000, 3000, 100)
	MobileAddProducer("ws://a:1", "0xa")
	MobileAddProducer("ws://b:1", "0xb")

	mobileMu.Lock()
	mobileSingleton.activeURI = "ws://a:1"
	// 10 seconds ago — way beyond slow timeout.
	mobileSingleton.lastBlock.Store(time.Now().UnixMilli() - 10_000)
	mobileMu.Unlock()

	rotationMonitorTick()

	mobileMu.Lock()
	got := mobileSingleton.activeURI
	mobileMu.Unlock()
	if got != "ws://a:1" {
		t.Errorf("fast-cycle should keep current active: got %q", got)
	}
}

// TestRotationTick_SlowSlowCycleRotates: leader is slow AND rotation
// cycle is slow → rotate to next.
func TestRotationTick_SlowSlowCycleRotates(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())
	// slow timeout = 3s, rotation cycle = 14s → rotate.
	MobileSetRotationPolicy(1000, 3000, 14000, 100)

	// Two unreachable producers (engine.Start will fail, but the test
	// checks the rotation DECISION via activeURI flip. We pre-set
	// activeURI manually so no real dial happens.)
	MobileAddProducer("ws://a:1", "0xa")
	MobileAddProducer("ws://b:1", "0xb")

	mobileMu.Lock()
	mobileSingleton.activeURI = "ws://a:1"
	mobileSingleton.lastBlock.Store(time.Now().UnixMilli() - 10_000)
	mobileMu.Unlock()

	rotationMonitorTick()

	// rotateNextLocked tried to start "ws://b:1" — that would fail with
	// a real dial, but the test only requires that the active flipped
	// AWAY from "ws://a:1". Acceptable values: "ws://b:1" (rotate
	// succeeded) OR "" (rotate started B, B's start failed, activeURI
	// reset). Either way, NOT "ws://a:1".
	mobileMu.Lock()
	got := mobileSingleton.activeURI
	mobileMu.Unlock()
	if got == "ws://a:1" {
		t.Errorf("expected rotation away from ws://a:1, still active: %q", got)
	}
}

// TestRotationTick_OneCandidateNoRotation: only one candidate → no
// rotation possible regardless of policy.
func TestRotationTick_OneCandidateNoRotation(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())
	MobileSetRotationPolicy(1000, 3000, 14000, 100)
	MobileAddProducer("ws://only:1", "0xa")

	mobileMu.Lock()
	mobileSingleton.activeURI = "ws://only:1"
	mobileSingleton.lastBlock.Store(time.Now().UnixMilli() - 30_000)
	mobileMu.Unlock()

	rotationMonitorTick()

	mobileMu.Lock()
	got := mobileSingleton.activeURI
	mobileMu.Unlock()
	if got != "ws://only:1" {
		t.Errorf("single candidate should never rotate: active = %q", got)
	}
}

// TestRotationTick_NoActiveNoOp: no active producer set → tick is a
// no-op (does not panic, does not start something randomly).
func TestRotationTick_NoActiveNoOp(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())
	MobileAddProducer("ws://a:1", "0xa")
	MobileAddProducer("ws://b:1", "0xb")

	rotationMonitorTick() // should not panic

	if got := MobileGetActiveProducer(); got != "" {
		t.Errorf("no active expected, got %q", got)
	}
}

// TestLastBlockUpdatedByCacheHook: when callCache fires, lastBlock
// timestamp gets bumped (via the dedup cache hook).
func TestLastBlockUpdatedByCacheHook(t *testing.T) {
	resetMobile(t)
	defer MobileFree()
	MobileInit(4242)
	MobileSetPrivKey(mobilePrivKeyHex())

	mobileMu.Lock()
	mobileSingleton.lastBlock.Store(0)
	mobileMu.Unlock()

	hash := types.HexToHash("0xfafafafafafafafafafafafafafafafafafafafafafafafafafafafafafafafa")
	callCache(123, hash, []byte("payload"))

	mobileMu.Lock()
	got := mobileSingleton.lastBlock.Load()
	mobileMu.Unlock()
	if got == 0 {
		t.Fatal("lastBlock was not bumped by callCache")
	}
	// Should be ≤ 100ms ago.
	delta := time.Now().UnixMilli() - got
	if delta < 0 || delta > 1000 {
		t.Errorf("lastBlock delta = %dms, expected ~now", delta)
	}
}
