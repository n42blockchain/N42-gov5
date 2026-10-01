// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/holiman/uint256"
)

func TestGraceCatchUpDisabledByDefault(t *testing.T) {
	os.Unsetenv(catchUpGraceEnvVar)
	if d := catchUpGraceDuration(); d != 0 {
		t.Fatalf("catchUpGraceDuration() = %v, want 0 when unset", d)
	}
	os.Setenv(catchUpGraceEnvVar, "0")
	defer os.Unsetenv(catchUpGraceEnvVar)
	if d := catchUpGraceDuration(); d != 0 {
		t.Fatalf("catchUpGraceDuration() = %v, want 0 for explicit 0", d)
	}
}

// TestGraceCatchUpDeferredAndResolvedByNormalPath: a lag-1 target is deferred
// under the grace; the head advances to the target before the grace expires
// (the normal proposal/commit path delivered it), so no range request should
// ever be issued — observed as catchUpInProgress never being taken.
func TestGraceCatchUpDeferredAndResolvedByNormalPath(t *testing.T) {
	os.Setenv(catchUpGraceEnvVar, "80")
	defer os.Unsetenv(catchUpGraceEnvVar)

	chain := &syncChainStub{current: &syncBlockStub{number: uint256.NewInt(100)}}
	svc := &Service{ctx: context.Background(), cfg: &config{chain: chain}}

	svc.enqueueCatchUp(101) // lag == 1, should be deferred, not issued

	if got := svc.catchUpGraceTarget.Load(); got != 101 {
		t.Fatalf("catchUpGraceTarget = %d, want 101 (deferred)", got)
	}
	if svc.catchUpInProgress.Load() {
		t.Fatal("catch-up started immediately despite grace period")
	}

	// Normal path delivers the block during the grace window.
	chain.current = &syncBlockStub{number: uint256.NewInt(101)}

	time.Sleep(160 * time.Millisecond) // well past the 80ms grace

	if svc.catchUpInProgress.Load() {
		t.Fatal("deferred catch-up fired even though the head caught up during the grace")
	}
	if got := svc.catchUpTarget.Load(); got != 0 {
		t.Fatalf("catchUpTarget = %d, want 0 (never handed to the immediate path)", got)
	}
}

// TestGraceCatchUpIssuedWhenHeadStillBehind: a lag-1 target is deferred, the
// head never advances, so once the grace expires the target must flow into
// the ordinary enqueueCatchUpNow coalescing path (observed via catchUpTarget,
// since catchUpInProgress is held busy to avoid touching the real p2p stack).
func TestGraceCatchUpIssuedWhenHeadStillBehind(t *testing.T) {
	os.Setenv(catchUpGraceEnvVar, "30")
	defer os.Unsetenv(catchUpGraceEnvVar)

	chain := &syncChainStub{current: &syncBlockStub{number: uint256.NewInt(200)}}
	svc := &Service{ctx: context.Background(), cfg: &config{chain: chain}}
	// Hold the owner flag so the eventual enqueueCatchUpNow only records the
	// coalesced target instead of reaching into the unwired p2p/chain fetch path.
	svc.catchUpInProgress.Store(true)

	svc.enqueueCatchUp(201) // lag == 1, deferred

	if svc.catchUpTarget.Load() != 0 {
		t.Fatal("target reached the immediate path before the grace expired")
	}

	deadline := time.Now().Add(time.Second)
	for svc.catchUpTarget.Load() != 201 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := svc.catchUpTarget.Load(); got != 201 {
		t.Fatalf("catchUpTarget after grace expiry = %d, want 201", got)
	}
}

// TestGraceCatchUpLagTwoIsImmediate: a lag >= 2 target must never be deferred,
// regardless of the grace setting.
func TestGraceCatchUpLagTwoIsImmediate(t *testing.T) {
	os.Setenv(catchUpGraceEnvVar, "5000")
	defer os.Unsetenv(catchUpGraceEnvVar)

	chain := &syncChainStub{current: &syncBlockStub{number: uint256.NewInt(300)}}
	svc := &Service{ctx: context.Background(), cfg: &config{chain: chain}}
	svc.catchUpInProgress.Store(true) // same isolation trick as above

	svc.enqueueCatchUp(302) // lag == 2

	if got := svc.catchUpTarget.Load(); got != 302 {
		t.Fatalf("catchUpTarget = %d, want 302 issued immediately for lag >= 2", got)
	}
	if got := svc.catchUpGraceTarget.Load(); got != 0 {
		t.Fatalf("catchUpGraceTarget = %d, want 0 (lag >= 2 must not defer)", got)
	}
}
