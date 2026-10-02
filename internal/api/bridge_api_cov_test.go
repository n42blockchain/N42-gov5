// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/bridge"
)

func TestBridgeAPI_NilRouterGuards(t *testing.T) {
	b := NewBridgeAPI(nil)

	if _, err := b.Send(context.Background(), 1, types.Address{}, uint256.NewInt(1)); err == nil {
		t.Fatal("Send(nil router) error = nil, want error")
	}
	if _, err := b.Status(context.Background(), types.Hash{}); err == nil {
		t.Fatal("Status(nil router) error = nil, want error")
	}
	if _, err := b.LatestVerifiedBlock(context.Background(), 1); err == nil {
		t.Fatal("LatestVerifiedBlock(nil router) error = nil, want error")
	}
	if _, err := b.RouteInfo(context.Background(), 1); err == nil {
		t.Fatal("RouteInfo(nil router) error = nil, want error")
	}

	apis := b.APIs()
	if len(apis) != 1 || apis[0].Namespace != "bridge" || apis[0].Service == nil {
		t.Fatalf("APIs() = %+v, want one bridge-namespace entry", apis)
	}
}

func TestBridgeAPI_SendZeroAmountRejected(t *testing.T) {
	// router is still nil here; the amount check fires first only when a
	// router *is* configured. With a nil router the "bridge not configured"
	// branch wins, which TestBridgeAPI_NilRouterGuards already covers. This
	// test instead exercises the zero/nil amount checks directly against a
	// configured but otherwise inert router stub.
	b := NewBridgeAPI(&inertBridgeRouter{})

	if _, err := b.Send(context.Background(), 1, types.Address{}, nil); err == nil {
		t.Fatal("Send(nil amount) error = nil, want error")
	}
	if _, err := b.Send(context.Background(), 1, types.Address{}, uint256.NewInt(0)); err == nil {
		t.Fatal("Send(zero amount) error = nil, want error")
	}
}

func TestBridgeAPI_RouteInfoUnsupportedRouter(t *testing.T) {
	b := NewBridgeAPI(&inertBridgeRouter{})
	if _, err := b.RouteInfo(context.Background(), 1); err == nil {
		t.Fatal("RouteInfo(non-ZKRouter) error = nil, want error")
	}
}

func TestBridgeStatusName(t *testing.T) {
	cases := map[bridge.BridgeStatus]string{
		bridge.StatusPending:    "pending",
		bridge.StatusProving:    "proving",
		bridge.StatusSubmitted:  "submitted",
		bridge.StatusVerified:   "verified",
		bridge.StatusCompleted:  "completed",
		bridge.StatusFailed:     "failed",
		bridge.BridgeStatus(99): "unknown",
	}
	for status, want := range cases {
		if got := bridgeStatusName(status); got != want {
			t.Fatalf("bridgeStatusName(%v) = %q, want %q", status, got, want)
		}
	}
}

// inertBridgeRouter is a minimal bridge.Router that never succeeds; used to
// reach the "router configured but request invalid" branches without
// depending on a real bridge implementation.
type inertBridgeRouter struct{}

func (inertBridgeRouter) Send(destChain uint32, recipient types.Address, amount *uint256.Int) (types.Hash, error) {
	return types.Hash{}, nil
}
func (inertBridgeRouter) Status(txHash types.Hash) (bridge.BridgeStatus, error) {
	return bridge.StatusPending, nil
}
func (inertBridgeRouter) LatestVerifiedBlock(destChain uint32) (uint64, error)    { return 0, nil }
func (inertBridgeRouter) VerifyIncoming(proof []byte, stateRoot types.Hash) error { return nil }

var _ bridge.Router = inertBridgeRouter{}
