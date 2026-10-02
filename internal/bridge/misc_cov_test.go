// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// misc_cov_test.go covers small pure helpers and remaining branches across
// the bridge package that did not fit naturally into the other coverage
// test files: DefaultPublisherConfig, fork-version selection, sync
// committee hashing, verified-root ring eviction and routeTypeName's
// default case.

package bridge

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestDefaultPublisherConfig(t *testing.T) {
	cfg := DefaultPublisherConfig()
	if cfg.BatchSize != 100 {
		t.Fatalf("BatchSize = %d, want 100", cfg.BatchSize)
	}
	if cfg.PollInterval.Seconds() != 12 {
		t.Fatalf("PollInterval = %v, want 12s", cfg.PollInterval)
	}
}

func TestForkVersionForSlot(t *testing.T) {
	cases := []struct {
		epoch uint64
		want  uint32
	}{
		{0, ForkVersionAltair},
		{EpochBellatrix, ForkVersionBellatrix},
		{EpochCapella, ForkVersionCapella},
		{EpochDeneb, ForkVersionDeneb},
		{EpochElectra, ForkVersionElectra},
	}
	for _, c := range cases {
		slot := c.epoch * SlotsPerEpoch
		if got := forkVersionForSlot(slot); got != c.want {
			t.Fatalf("forkVersionForSlot(%d) = %#x, want %#x", slot, got, c.want)
		}
	}
}

func TestHashSyncCommittee(t *testing.T) {
	c1 := &SyncCommittee{AggregatePubKey: []byte{0x1}}
	c2 := &SyncCommittee{AggregatePubKey: []byte{0x2}}
	h1 := hashSyncCommittee(c1)
	h2 := hashSyncCommittee(c2)
	if h1 == h2 {
		t.Fatal("different committees should hash differently")
	}
	// Deterministic.
	if hashSyncCommittee(c1) != h1 {
		t.Fatal("hashSyncCommittee should be deterministic")
	}
}

func TestRouteTypeName_Unknown(t *testing.T) {
	if got := routeTypeName(RouteType(42)); got != "Unknown" {
		t.Fatalf("routeTypeName(42) = %q, want Unknown", got)
	}
	if got := routeTypeName(RouteZK); got != "ZK" {
		t.Fatalf("routeTypeName(RouteZK) = %q, want ZK", got)
	}
	if got := routeTypeName(RouteHyperlane); got != "Hyperlane" {
		t.Fatalf("routeTypeName(RouteHyperlane) = %q, want Hyperlane", got)
	}
}

func TestEthLightClient_AddVerifiedRoot_DuplicateUpdatesInPlace(t *testing.T) {
	verifier := &covFakeBLSVerifier{}
	lc, err := NewEthLightClient(&EthLightClientConfig{
		InitialCommittee: newTestCommittee(),
		InitialHeader:    &EthHeader{Slot: 1, StateRoot: types.Hash{0x1}},
	}, verifier)
	if err != nil {
		t.Fatalf("NewEthLightClient: %v", err)
	}

	// Re-adding the same slot with a different root should update in place,
	// not grow the ring.
	lc.addVerifiedRoot(1, types.Hash{0x2})
	root, ok := lc.GetVerifiedStateRoot(1)
	if !ok || root != (types.Hash{0x2}) {
		t.Fatalf("GetVerifiedStateRoot(1) = %x, %v; want updated root", root, ok)
	}
}

func TestVerifyHeaderChainLocally_EmptyHeaders(t *testing.T) {
	if err := VerifyHeaderChainLocally(nil, nil, nil); err != nil {
		t.Fatalf("expected no error for empty header/QC slices, got %v", err)
	}
}

func TestVerifyHeaderChainLocally_MismatchedLengths(t *testing.T) {
	chain := newScriptedChain()
	chain.appendBlocks(1)
	headers, _, err := fetchHeadersAndQCs(chain, 1, 1)
	if err != nil {
		t.Fatalf("fetchHeadersAndQCs: %v", err)
	}
	if err := VerifyHeaderChainLocally(headers, nil, nil); err == nil {
		t.Fatal("expected error for header/QC count mismatch")
	}
}
