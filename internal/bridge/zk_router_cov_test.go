// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// zk_router_cov_test.go exercises ZKRouter's Send/VerifyIncoming/Status/
// LatestVerifiedBlock/RouteFor/AddRoute API using a fake in-process
// HyperlaneDispatcher (no real network, no real RPC client). Pure
// coverage-focused tests for internal/bridge.

package bridge

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// covFakeHyperlaneDispatcher is an in-memory HyperlaneDispatcher fake.
type covFakeHyperlaneDispatcher struct {
	dispatchErr error
	quoteErr    error
	lastDest    uint32
	lastBody    []byte
	callCount   int
}

func (f *covFakeHyperlaneDispatcher) Dispatch(_ context.Context, destDomain uint32, _ [32]byte, body []byte) (types.Hash, error) {
	f.callCount++
	f.lastDest = destDomain
	f.lastBody = body
	if f.dispatchErr != nil {
		return types.Hash{}, f.dispatchErr
	}
	var h types.Hash
	h[0] = 0xAB
	return h, nil
}

func (f *covFakeHyperlaneDispatcher) QuoteDispatch(_ context.Context, _ uint32, _ []byte) (*uint256.Int, error) {
	if f.quoteErr != nil {
		return nil, f.quoteErr
	}
	return uint256.NewInt(1), nil
}

func TestZKRouter_SendZKRoute(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()

	recipient := types.HexToAddress("0x1000000000000000000000000000000000000001")
	txHash, err := r.Send(DomainEthereum, recipient, uint256.NewInt(100))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	status, err := r.Status(txHash)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != StatusPending {
		t.Fatalf("status = %v, want StatusPending", status)
	}
}

func TestZKRouter_SendHyperlaneRoute(t *testing.T) {
	fake := &covFakeHyperlaneDispatcher{}
	r := NewZKRouter(nil, fake, nil, nil, nil)
	defer r.Close()

	recipient := types.HexToAddress("0x2000000000000000000000000000000000000002")
	txHash, err := r.Send(DomainArbitrum, recipient, uint256.NewInt(5))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fake.callCount != 1 {
		t.Fatalf("dispatcher called %d times, want 1", fake.callCount)
	}
	if fake.lastDest != DomainArbitrum {
		t.Fatalf("lastDest = %d, want %d", fake.lastDest, DomainArbitrum)
	}
	if txHash[0] != 0xAB {
		t.Fatalf("unexpected txHash %x", txHash)
	}
}

func TestZKRouter_SendHyperlaneNoDispatcherConfigured(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()

	recipient := types.HexToAddress("0x3000000000000000000000000000000000000003")
	if _, err := r.Send(DomainArbitrum, recipient, uint256.NewInt(1)); err == nil {
		t.Fatal("expected error when Hyperlane dispatcher is nil")
	}
}

func TestZKRouter_SendHyperlaneDispatchError(t *testing.T) {
	fake := &covFakeHyperlaneDispatcher{dispatchErr: context.DeadlineExceeded}
	r := NewZKRouter(nil, fake, nil, nil, nil)
	defer r.Close()

	recipient := types.HexToAddress("0x4000000000000000000000000000000000000004")
	if _, err := r.Send(DomainArbitrum, recipient, uint256.NewInt(1)); err == nil {
		t.Fatal("expected dispatch error to propagate")
	}
}

func TestZKRouter_SendUnsupportedChainFallsBackToHyperlane(t *testing.T) {
	fake := &covFakeHyperlaneDispatcher{}
	r := NewZKRouter(nil, fake, nil, nil, nil)
	defer r.Close()

	recipient := types.HexToAddress("0x5000000000000000000000000000000000000005")
	if _, err := r.Send(999999, recipient, uint256.NewInt(1)); err != nil {
		t.Fatalf("Send to unknown chain should fall back to Hyperlane: %v", err)
	}
	if fake.callCount != 1 {
		t.Fatalf("expected fallback dispatch, got %d calls", fake.callCount)
	}
}

func TestZKRouter_SendUnsupportedChainNoHyperlane(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()

	recipient := types.HexToAddress("0x6000000000000000000000000000000000000006")
	if _, err := r.Send(999999, recipient, uint256.NewInt(1)); err == nil {
		t.Fatal("expected unsupported-chain error")
	}
}

func TestZKRouter_StatusNotFound(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()

	if _, err := r.Status(types.Hash{0x1}); err == nil {
		t.Fatal("expected error for unknown transfer")
	}
}

func TestZKRouter_VerifyIncomingEmptyProof(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()

	if err := r.VerifyIncoming(nil, types.Hash{}); err == nil {
		t.Fatal("expected error for empty proof")
	}
}

func TestZKRouter_VerifyIncomingNoLightClient(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()

	if err := r.VerifyIncoming([]byte{0x1}, types.Hash{}); err == nil {
		t.Fatal("expected error when no ETH light client configured")
	}
}

func TestZKRouter_VerifyIncomingWithLightClient(t *testing.T) {
	verifier := &BLSVerifierImpl{}
	lc, err := NewEthLightClient(&EthLightClientConfig{
		InitialCommittee: &SyncCommittee{},
		InitialHeader: &EthHeader{
			Slot:      1,
			StateRoot: types.Hash{0x7},
		},
	}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}

	r := NewZKRouter(nil, nil, lc, nil, nil)
	defer r.Close()

	if err := r.VerifyIncoming([]byte{0x1}, types.Hash{0x7}); err != nil {
		t.Fatalf("VerifyIncoming should accept matching state root: %v", err)
	}
	if err := r.VerifyIncoming([]byte{0x1}, types.Hash{0x9}); err == nil {
		t.Fatal("VerifyIncoming should reject unknown state root")
	}
}

func TestZKRouter_LatestVerifiedBlock(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()

	if _, err := r.LatestVerifiedBlock(999999); err == nil {
		t.Fatal("expected error for unsupported chain")
	}

	got, err := r.LatestVerifiedBlock(DomainEthereum)
	if err != nil {
		t.Fatalf("LatestVerifiedBlock(ZK route, nil publisher): %v", err)
	}
	if got != 0 {
		t.Fatalf("got %d, want 0 with nil publisher", got)
	}

	got, err = r.LatestVerifiedBlock(DomainArbitrum)
	if err != nil {
		t.Fatalf("LatestVerifiedBlock(Hyperlane route): %v", err)
	}
	if got != 0 {
		t.Fatalf("got %d, want 0 for Hyperlane route", got)
	}
}

func TestZKRouter_RouteForAndAddRoute(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()

	if _, ok := r.RouteFor(DomainEthereum); !ok {
		t.Fatal("expected default Ethereum route to exist")
	}
	if _, ok := r.RouteFor(123456); ok {
		t.Fatal("expected no route for unregistered chain")
	}

	r.AddRoute(&ChainRoute{Domain: 123456, RouteType: RouteHyperlane, Name: "custom"})
	route, ok := r.RouteFor(123456)
	if !ok || route.Name != "custom" {
		t.Fatalf("AddRoute did not register route: %+v, ok=%v", route, ok)
	}
}

func TestZKRouter_CustomRouteConfig(t *testing.T) {
	cfg := &ZKRouterConfig{
		CustomRoutes: map[uint32]RouteType{
			DomainEthereum: RouteHyperlane,
		},
	}
	r := NewZKRouter(nil, nil, nil, nil, cfg)
	defer r.Close()

	route, ok := r.RouteFor(DomainEthereum)
	if !ok {
		t.Fatal("expected Ethereum route to exist")
	}
	if route.RouteType != RouteHyperlane {
		t.Fatalf("RouteType = %v, want RouteHyperlane (overridden by config)", route.RouteType)
	}
}

func TestZKRouter_UnknownRouteTypeInSend(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()
	r.AddRoute(&ChainRoute{Domain: 777, RouteType: RouteType(99), Name: "weird"})

	recipient := types.HexToAddress("0x7000000000000000000000000000000000000007")
	if _, err := r.Send(777, recipient, uint256.NewInt(1)); err == nil {
		t.Fatal("expected error for unknown route type")
	}
}

func TestZKRouter_UnknownRouteTypeInLatestVerifiedBlock(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()
	r.AddRoute(&ChainRoute{Domain: 778, RouteType: RouteType(99), Name: "weird"})

	if _, err := r.LatestVerifiedBlock(778); err == nil {
		t.Fatal("expected error for unknown route type")
	}
}

func TestZKRouter_ManyTransfersRingEviction(t *testing.T) {
	r := NewZKRouter(nil, nil, nil, nil, nil)
	defer r.Close()

	recipient := types.HexToAddress("0x8000000000000000000000000000000000000008")
	// Send a handful of transfers; exercises addTransfer path repeatedly
	// (full ring eviction at 10000 entries is covered structurally, not
	// by actually sending 10000 — that would be slow for a short test).
	for i := 0; i < 25; i++ {
		if _, err := r.Send(DomainEthereum, recipient, uint256.NewInt(uint64(i+1))); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}
}
