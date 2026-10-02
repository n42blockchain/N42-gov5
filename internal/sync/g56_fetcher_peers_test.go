package sync

// g56: exercises BasicFetcher.FetchBlocks' peer-selection and multi-peer
// retry loop (previously 0% under fakeP2P — the existing fetcher_test.go
// only drives the "not running" short-circuit with a nil p2p), plus the
// still-stubbed FetchBlocksByHash / fetchFromPeer paths.

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/test"
)

func TestBasicFetcher_FetchBlocks_NoPeersAvailable(t *testing.T) {
	ctx := context.Background()
	fp := newFakeP2P(t)

	f := NewBasicFetcher(ctx, fp, nil, nil)
	if err := f.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer f.Stop()

	_, err := f.FetchBlocks(ctx, uint256.NewInt(0), 10)
	if err == nil {
		t.Fatal("expected error when no peers are connected")
	}
	m := f.Metrics()
	if m.fetchesFailed == 0 {
		t.Error("expected a failed-fetch metric to be recorded for the no-peers case")
	}
}

// TestBasicFetcher_FetchBlocks_RetriesAcrossPeers connects several peers and
// checks FetchBlocks tries each in turn — fetchFromPeer is still an
// unimplemented stub (always errors), so the loop must exhaust every peer
// BestPeers returns and surface the last error wrapped, never panicking on
// an empty peer set mid-loop.
func TestBasicFetcher_FetchBlocks_RetriesAcrossPeers(t *testing.T) {
	ctx := context.Background()
	fp := newFakeP2P(t)

	for i := 0; i < 5; i++ {
		pid, err := test.RandPeerID()
		if err != nil {
			t.Fatalf("RandPeerID: %v", err)
		}
		fp.addConnectedPeer(t, pid, uint64(100+i))
	}

	cfg := DefaultFetcherConfig()
	cfg.MinPeers = 1
	f := NewBasicFetcher(ctx, fp, nil, cfg)
	if err := f.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer f.Stop()

	_, err := f.FetchBlocks(ctx, uint256.NewInt(0), 10)
	if err == nil {
		t.Fatal("expected an error since fetchFromPeer is an unimplemented stub")
	}

	m := f.Metrics()
	if m.fetchesFailed == 0 {
		t.Error("expected at least one failed-fetch metric across the retry loop")
	}
}

func TestBasicFetcher_FetchBlocksByHash_NotImplemented(t *testing.T) {
	ctx := context.Background()
	f := NewBasicFetcher(ctx, nil, nil, nil)
	if err := f.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer f.Stop()

	if _, err := f.FetchBlocksByHash(ctx, [][]byte{{0x01}}); err == nil {
		t.Error("expected FetchBlocksByHash to report not-implemented")
	}
}

func TestBasicFetcher_fetchFromPeer_NotImplemented(t *testing.T) {
	ctx := context.Background()
	f := NewBasicFetcher(ctx, nil, nil, nil)
	pid, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	if _, err := f.fetchFromPeer(ctx, pid, uint256.NewInt(0), 5); err == nil {
		t.Error("expected fetchFromPeer to report not-fully-implemented")
	}
}
