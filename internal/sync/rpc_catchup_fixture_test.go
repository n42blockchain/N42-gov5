package sync

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/test"
)

// newCatchUpTestService builds a *Service wired to a fakeP2P and a chain at
// the given head height, for HeightBehind/CatchUp/CatchUpTo tests that must
// not reach the real network fetch path.
func newCatchUpTestService(t *testing.T, headHeight uint64) (*Service, *fakeP2P, *syncChainStub) {
	t.Helper()
	fp := newFakeP2P(t)
	chain := &syncChainStub{current: &syncBlockStub{number: uint256.NewInt(headHeight)}}
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp, chain: chain}}
	return svc, fp, chain
}

func TestHeightBehindNoPeers(t *testing.T) {
	svc, _, _ := newCatchUpTestService(t, 100)
	if got := svc.HeightBehind(); got != 0 {
		t.Fatalf("HeightBehind() = %d, want 0 with no peers", got)
	}
}

func TestHeightBehindPeersNotAhead(t *testing.T) {
	svc, fp, _ := newCatchUpTestService(t, 100)
	peerID, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, peerID, 100) // same height, not ahead
	if got := svc.HeightBehind(); got != 0 {
		t.Fatalf("HeightBehind() = %d, want 0 when no peer is ahead", got)
	}
}

func TestHeightBehindPeerAhead(t *testing.T) {
	svc, fp, _ := newCatchUpTestService(t, 100)
	peerID, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, peerID, 115)
	if got := svc.HeightBehind(); got != 15 {
		t.Fatalf("HeightBehind() = %d, want 15", got)
	}
}

func TestCatchUpNoOpWhenNoPeerAhead(t *testing.T) {
	svc, _, _ := newCatchUpTestService(t, 100)
	svc.CatchUp() // must return without touching the network path
	if svc.catchUpTarget.Load() != 0 {
		t.Fatalf("catchUpTarget = %d, want 0 (no peer ahead)", svc.catchUpTarget.Load())
	}
}

func TestCatchUpEnqueuesWhenPeerAhead(t *testing.T) {
	svc, fp, _ := newCatchUpTestService(t, 100)
	peerID, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, peerID, 110)
	// Hold the in-progress flag so enqueueCatchUpNow only coalesces the
	// target instead of reaching into catchUpTo's network fetch.
	svc.catchUpInProgress.Store(true)

	svc.CatchUp()

	if got := svc.catchUpTarget.Load(); got != 110 {
		t.Fatalf("catchUpTarget = %d, want 110", got)
	}
}

func TestCatchUpToNoOpWhenAtOrAboveTarget(t *testing.T) {
	svc, _, _ := newCatchUpTestService(t, 100)
	svc.catchUpInProgress.Store(true)
	svc.CatchUpTo(100)
	if svc.catchUpTarget.Load() != 0 {
		t.Fatalf("catchUpTarget = %d, want 0 when target == self", svc.catchUpTarget.Load())
	}
	svc.CatchUpTo(50)
	if svc.catchUpTarget.Load() != 0 {
		t.Fatalf("catchUpTarget = %d, want 0 when target < self", svc.catchUpTarget.Load())
	}
}

func TestCatchUpToEnqueuesAboveSelf(t *testing.T) {
	svc, _, _ := newCatchUpTestService(t, 100)
	svc.catchUpInProgress.Store(true)
	svc.CatchUpTo(150)
	if got := svc.catchUpTarget.Load(); got != 150 {
		t.Fatalf("catchUpTarget = %d, want 150", got)
	}
}

func TestCatchUpTickNoOpWithoutBlockImportNotifier(t *testing.T) {
	svc, fp, _ := newCatchUpTestService(t, 100)
	peerID, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, peerID, 200)
	svc.catchUpTick() // no notifier registered -> must be a no-op
	if svc.catchUpTarget.Load() != 0 {
		t.Fatalf("catchUpTarget = %d, want 0 without a block import notifier", svc.catchUpTarget.Load())
	}
}
