package peers

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

func chainStateAtHeight(h uint64) *sync_pb.Status {
	return &sync_pb.Status{CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(h))}
}

func TestStatus_Bad(t *testing.T) {
	st := newTestStatus(t, 10)
	// With no peers scored bad, Bad() should return an empty (non-nil-panicking) slice.
	if got := st.Bad(); len(got) != 0 {
		t.Fatalf("Bad() = %v, want empty", got)
	}
}

func TestStatus_SameIPAndAddIPToTracker(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	a := addr(t, "/ip4/9.9.9.9/tcp/1")

	st.Add(nil, pid, a, network.DirInbound)
	// A second peer sharing the same IP should push the tracker above 1.
	pid2 := randPeerID(t)
	st.Add(nil, pid2, a, network.DirInbound)

	if st.isfromBadIP(pid) {
		t.Fatal("two peers on one IP should not yet exceed ColocationLimit")
	}

	// Loopback addresses must be excluded from the tracker.
	pidLoop := randPeerID(t)
	st.Add(nil, pidLoop, addr(t, "/ip4/127.0.0.1/tcp/1"), network.DirInbound)
	if st.isfromBadIP(pidLoop) {
		t.Fatal("loopback peer should never be considered a bad-IP peer via tracker")
	}
}

func TestStatus_IsFromBadIPExceedsLimit(t *testing.T) {
	st := newTestStatus(t, 50)
	ip := "/ip4/8.8.4.4/tcp/"
	var last peer.ID
	for i := 0; i < ColocationLimit+2; i++ {
		pid := randPeerID(t)
		st.Add(nil, pid, addr(t, ip+"1"), network.DirInbound)
		last = pid
	}
	if !st.isfromBadIP(last) {
		t.Fatal("expected peer to be flagged from a bad (over-colocated) IP")
	}
}

func TestStatus_UnknownPeerIsNotBadIP(t *testing.T) {
	st := newTestStatus(t, 10)
	if st.isfromBadIP(randPeerID(t)) {
		t.Fatal("unknown peer should not be considered a bad-IP peer")
	}
}

func TestStatus_TallyIPTrackerViaPrune(t *testing.T) {
	// Prune() calls tallyIPTracker() as its last step; drive it through a
	// prune that actually removes peers to exercise that path.
	st := newTestStatus(t, 1)
	for i := 0; i < 3; i++ {
		pid := randPeerID(t)
		st.Add(nil, pid, addr(t, "/ip4/1.1.1.1/tcp/1"), network.DirInbound)
		st.SetConnectionState(pid, PeerDisconnected)
	}
	st.Prune()
	if len(st.All()) > 1 {
		t.Fatalf("expected Prune to cut down to MaxPeers=1, got %d", len(st.All()))
	}
}

func TestStatus_PruneNoOpUnderLimit(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	st.Add(nil, pid, addr(t, "/ip4/1.1.1.1/tcp/1"), network.DirInbound)
	st.Prune() // under MaxPeers: must be a no-op, not panic.
	if len(st.All()) != 1 {
		t.Fatalf("expected Prune to be a no-op under the peer limit, got %d peers", len(st.All()))
	}
}

func TestStatus_BestPeers(t *testing.T) {
	st := newTestStatus(t, 10)

	lower := randPeerID(t)
	higher := randPeerID(t)
	noState := randPeerID(t)

	st.Add(nil, lower, addr(t, "/ip4/1.0.0.1/tcp/1"), network.DirInbound)
	st.SetConnectionState(lower, PeerConnected)
	st.SetChainState(lower, chainStateAtHeight(50))

	st.Add(nil, higher, addr(t, "/ip4/1.0.0.2/tcp/1"), network.DirInbound)
	st.SetConnectionState(higher, PeerConnected)
	st.SetChainState(higher, chainStateAtHeight(200))

	st.Add(nil, noState, addr(t, "/ip4/1.0.0.3/tcp/1"), network.DirInbound)
	st.SetConnectionState(noState, PeerConnected)
	// no chain state set for noState: must be skipped, not panic.

	target, pids := st.BestPeers(5, uint256.NewInt(10))
	if target.Uint64() != 200 {
		t.Fatalf("BestPeers target = %d, want 200", target.Uint64())
	}
	found := false
	for _, p := range pids {
		if p == higher {
			found = true
		}
		if p == noState {
			t.Fatal("peer without chain state should not appear in BestPeers")
		}
	}
	if !found {
		t.Fatal("expected the higher peer to be included in BestPeers")
	}
}

func TestStatus_BestPeersNoneAboveOurs(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	st.Add(nil, pid, addr(t, "/ip4/1.0.0.1/tcp/1"), network.DirInbound)
	st.SetConnectionState(pid, PeerConnected)
	st.SetChainState(pid, chainStateAtHeight(5))

	target, pids := st.BestPeers(5, uint256.NewInt(100))
	if target.Uint64() != 0 {
		t.Fatalf("BestPeers target = %d, want 0 when no peer is ahead", target.Uint64())
	}
	if len(pids) != 0 {
		t.Fatalf("expected no candidate peers, got %d", len(pids))
	}
}

func TestStatus_HighestBlockNumber(t *testing.T) {
	st := newTestStatus(t, 10)
	if st.HighestBlockNumber().Uint64() != 0 {
		t.Fatal("expected 0 with no peers")
	}

	a := randPeerID(t)
	b := randPeerID(t)
	st.Add(nil, a, addr(t, "/ip4/1.0.0.1/tcp/1"), network.DirInbound)
	st.SetChainState(a, chainStateAtHeight(30))
	st.Add(nil, b, addr(t, "/ip4/1.0.0.2/tcp/1"), network.DirInbound)
	st.SetChainState(b, chainStateAtHeight(90))

	// A peer with no chain state at all must not crash HighestBlockNumber.
	c := randPeerID(t)
	st.Add(nil, c, addr(t, "/ip4/1.0.0.3/tcp/1"), network.DirInbound)

	if got := st.HighestBlockNumber().Uint64(); got != 90 {
		t.Fatalf("HighestBlockNumber() = %d, want 90", got)
	}
}

func TestStatus_PeersToPruneUnderLimit(t *testing.T) {
	st := newTestStatus(t, 100)
	pid := randPeerID(t)
	st.Add(nil, pid, addr(t, "/ip4/1.0.0.1/tcp/1"), network.DirInbound)
	st.SetConnectionState(pid, PeerConnected)

	if got := st.PeersToPrune(); len(got) != 0 {
		t.Fatalf("PeersToPrune() = %v, want empty when under connection limit", got)
	}
}

func TestStatus_PeersToPruneExcessInbound(t *testing.T) {
	// A tiny peer limit forces ConnectedPeerLimit low enough that a handful
	// of connected inbound peers exceed it, driving PeersToPrune's excess
	// calculation and its trusted-peer exemption.
	st := newTestStatus(t, 2)

	var trusted peer.ID
	for i := 0; i < 4; i++ {
		pid := randPeerID(t)
		st.Add(nil, pid, addr(t, "/ip4/1.0.0.1/tcp/1"), network.DirInbound)
		st.SetConnectionState(pid, PeerConnected)
		if i == 0 {
			trusted = pid
			st.SetTrusted([]peer.ID{trusted})
		}
	}

	toPrune := st.PeersToPrune()
	for _, pid := range toPrune {
		if pid == trusted {
			t.Fatal("trusted peer must never be selected for pruning")
		}
	}
}

func TestSameIP(t *testing.T) {
	a := addr(t, "/ip4/1.2.3.4/tcp/1")
	b := addr(t, "/ip4/1.2.3.4/tcp/2")
	c := addr(t, "/ip4/5.6.7.8/tcp/1")

	if !sameIP(a, b) {
		t.Fatal("expected same IP for differing ports")
	}
	if sameIP(a, c) {
		t.Fatal("expected different IP to report false")
	}
	if sameIP(nil, b) || sameIP(a, nil) {
		t.Fatal("expected nil multiaddr to report false")
	}
}
