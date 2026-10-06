package peers

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/n42blockchain/N42/internal/p2p/peers/peerdata"
	"github.com/n42blockchain/N42/internal/p2p/peers/scorers"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

func newTestStatus(t *testing.T, peerLimit int) *Status {
	t.Helper()
	return NewStatus(context.Background(), &StatusConfig{
		PeerLimit:    peerLimit,
		ScorerParams: &scorers.Config{},
	})
}

func randPeerID(t *testing.T) peer.ID {
	t.Helper()
	id, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	return id
}

func addr(t *testing.T, s string) ma.Multiaddr {
	t.Helper()
	a, err := ma.NewMultiaddr(s)
	if err != nil {
		t.Fatalf("NewMultiaddr(%s): %v", s, err)
	}
	return a
}

func TestStatus_AddAndQuery(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	a := addr(t, "/ip4/1.2.3.4/tcp/30303")

	st.Add(nil, pid, a, network.DirInbound)

	gotAddr, err := st.Address(pid)
	if err != nil || !gotAddr.Equal(a) {
		t.Fatalf("Address() = %v, err=%v", gotAddr, err)
	}

	dir, err := st.Direction(pid)
	if err != nil || dir != network.DirInbound {
		t.Fatalf("Direction() = %v, err=%v", dir, err)
	}

	gotEnr, err := st.ENR(pid)
	if err != nil || gotEnr != nil {
		t.Fatalf("ENR() = %v, err=%v, want nil record", gotEnr, err)
	}

	ip, err := st.IP(pid)
	if err != nil || ip.String() != "1.2.3.4" {
		t.Fatalf("IP() = %v, err=%v", ip, err)
	}

	dialArgs, err := st.DialArgs(pid)
	if err != nil || dialArgs == "" {
		t.Fatalf("DialArgs() = %q, err=%v", dialArgs, err)
	}

	cs, err := st.ConnState(pid)
	if err != nil || cs != PeerDisconnected {
		t.Fatalf("ConnState() = %v, err=%v, want PeerDisconnected", cs, err)
	}
}

func TestStatus_AddUpdatesExisting(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	a1 := addr(t, "/ip4/1.2.3.4/tcp/30303")
	a2 := addr(t, "/ip4/5.6.7.8/tcp/30303")

	st.Add(nil, pid, a1, network.DirInbound)
	st.Add(nil, pid, a2, network.DirOutbound)

	gotAddr, err := st.Address(pid)
	if err != nil || !gotAddr.Equal(a2) {
		t.Fatalf("Address() after update = %v, err=%v, want %v", gotAddr, err, a2)
	}
	dir, err := st.Direction(pid)
	if err != nil || dir != network.DirOutbound {
		t.Fatalf("Direction() after update = %v, err=%v", dir, err)
	}
}

func TestStatus_UnknownPeerErrors(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)

	if _, err := st.Address(pid); err != peerdata.ErrPeerUnknown {
		t.Fatalf("Address() err = %v, want ErrPeerUnknown", err)
	}
	if _, err := st.Direction(pid); err != peerdata.ErrPeerUnknown {
		t.Fatalf("Direction() err = %v, want ErrPeerUnknown", err)
	}
	if _, err := st.ENR(pid); err != peerdata.ErrPeerUnknown {
		t.Fatalf("ENR() err = %v, want ErrPeerUnknown", err)
	}
	if _, err := st.IP(pid); err != peerdata.ErrPeerUnknown {
		t.Fatalf("IP() err = %v, want ErrPeerUnknown", err)
	}
	if _, err := st.DialArgs(pid); err != peerdata.ErrPeerUnknown {
		t.Fatalf("DialArgs() err = %v, want ErrPeerUnknown", err)
	}
	if _, err := st.ConnState(pid); err != peerdata.ErrPeerUnknown {
		t.Fatalf("ConnState() err = %v, want ErrPeerUnknown", err)
	}
	if _, err := st.ConnectionState(pid); err != peerdata.ErrPeerUnknown {
		t.Fatalf("ConnectionState() err = %v, want ErrPeerUnknown", err)
	}
	if _, err := st.NextValidTime(pid); err != peerdata.ErrPeerUnknown {
		t.Fatalf("NextValidTime() err = %v, want ErrPeerUnknown", err)
	}
	if _, err := st.ChainStateLastUpdated(pid); err != peerdata.ErrPeerUnknown {
		t.Fatalf("ChainStateLastUpdated() err = %v, want ErrPeerUnknown", err)
	}
	if st.IsActive(pid) {
		t.Fatal("IsActive() on unknown peer should be false")
	}
	if st.IsBad(pid) {
		t.Fatal("IsBad() on unknown peer should be false")
	}
}

func TestStatus_ConnectionStateTransitions(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	st.Add(nil, pid, addr(t, "/ip4/1.2.3.4/tcp/1"), network.DirInbound)

	st.SetConnectionState(pid, PeerConnecting)
	cs, err := st.ConnectionState(pid)
	if err != nil || cs != PeerConnecting {
		t.Fatalf("ConnectionState() = %v, err=%v", cs, err)
	}
	if !st.IsActive(pid) {
		t.Fatal("expected peer in PeerConnecting to be active")
	}

	st.SetConnectionState(pid, PeerConnected)
	if !st.IsActive(pid) {
		t.Fatal("expected peer in PeerConnected to be active")
	}

	st.SetConnectionState(pid, PeerDisconnected)
	if st.IsActive(pid) {
		t.Fatal("expected disconnected peer to be inactive")
	}
}

func TestStatus_PingRoundTrip(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	st.Add(nil, pid, addr(t, "/ip4/1.2.3.4/tcp/1"), network.DirInbound)

	got, err := st.GetPing(pid)
	if err != nil || got != nil {
		t.Fatalf("GetPing() = %v, err=%v, want nil,nil before any ping set", got, err)
	}

	ping := &sync_pb.Ping{SeqNumber: 42}
	st.SetPing(pid, ping)

	got, err = st.GetPing(pid)
	if err != nil {
		t.Fatalf("GetPing(): %v", err)
	}
	if got == nil || got.SeqNumber != 42 {
		t.Fatalf("GetPing() = %v, want SeqNumber=42", got)
	}
}

func TestStatus_SetPingCreatesPeer(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	// SetPing on an unknown peer must create it (PeerDataGetOrCreate).
	st.SetPing(pid, &sync_pb.Ping{SeqNumber: 1})
	got, err := st.GetPing(pid)
	if err != nil || got == nil || got.SeqNumber != 1 {
		t.Fatalf("GetPing() after SetPing-create = %v, err=%v", got, err)
	}
}

func TestStatus_TrustedPeers(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	other := randPeerID(t)

	if st.IsTrusted(pid) {
		t.Fatal("expected not trusted before SetTrusted")
	}
	st.SetTrusted([]peer.ID{pid})
	if !st.IsTrusted(pid) {
		t.Fatal("expected pid to be trusted")
	}
	if st.IsTrusted(other) {
		t.Fatal("expected other to not be trusted")
	}
}

func TestStatus_BackOffAndReadyToDial(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	st.Add(nil, pid, addr(t, "/ip4/1.2.3.4/tcp/1"), network.DirInbound)

	if !st.IsReadyToDial(pid) {
		t.Fatal("expected a fresh peer to be ready to dial")
	}

	st.RandomizeBackOff(pid)
	nv, err := st.NextValidTime(pid)
	if err != nil {
		t.Fatalf("NextValidTime: %v", err)
	}
	if !nv.After(time.Now()) {
		t.Fatal("expected NextValidTime to be in the future after RandomizeBackOff")
	}
	if st.IsReadyToDial(pid) {
		t.Fatal("expected peer to not be ready to dial during backoff")
	}

	st.SetNextValidTime(pid, time.Now().Add(-time.Second))
	if !st.IsReadyToDial(pid) {
		t.Fatal("expected peer to be ready to dial after backoff expires")
	}
}

func TestStatus_BuckettedViews(t *testing.T) {
	st := newTestStatus(t, 10)

	inConnected := randPeerID(t)
	outConnected := randPeerID(t)
	connecting := randPeerID(t)
	disconnecting := randPeerID(t)
	disconnected := randPeerID(t)

	st.Add(nil, inConnected, addr(t, "/ip4/1.0.0.1/tcp/1"), network.DirInbound)
	st.SetConnectionState(inConnected, PeerConnected)

	st.Add(nil, outConnected, addr(t, "/ip4/1.0.0.2/tcp/1"), network.DirOutbound)
	st.SetConnectionState(outConnected, PeerConnected)

	st.Add(nil, connecting, addr(t, "/ip4/1.0.0.3/tcp/1"), network.DirInbound)
	st.SetConnectionState(connecting, PeerConnecting)

	st.Add(nil, disconnecting, addr(t, "/ip4/1.0.0.4/tcp/1"), network.DirInbound)
	st.SetConnectionState(disconnecting, PeerDisconnecting)

	st.Add(nil, disconnected, addr(t, "/ip4/1.0.0.5/tcp/1"), network.DirInbound)
	st.SetConnectionState(disconnected, PeerDisconnected)

	assertContains := func(name string, ids []peer.ID, want peer.ID) {
		for _, id := range ids {
			if id == want {
				return
			}
		}
		t.Fatalf("%s does not contain expected peer", name)
	}

	assertContains("Connected", st.Connected(), inConnected)
	assertContains("Connected", st.Connected(), outConnected)
	assertContains("Inbound", st.Inbound(), inConnected)
	assertContains("InboundConnected", st.InboundConnected(), inConnected)
	assertContains("Outbound", st.Outbound(), outConnected)
	assertContains("OutboundConnected", st.OutboundConnected(), outConnected)
	assertContains("Connecting", st.Connecting(), connecting)
	assertContains("Active", st.Active(), connecting)
	assertContains("Active", st.Active(), inConnected)
	assertContains("Disconnecting", st.Disconnecting(), disconnecting)
	assertContains("Disconnected", st.Disconnected(), disconnected)
	assertContains("Inactive", st.Inactive(), disconnecting)
	assertContains("Inactive", st.Inactive(), disconnected)

	all := st.All()
	if len(all) != 5 {
		t.Fatalf("All() returned %d peers, want 5", len(all))
	}
}

func TestStatus_MaxPeerLimitAndInboundLimit(t *testing.T) {
	st := newTestStatus(t, 20)
	if st.MaxPeerLimit() != 20 {
		t.Fatalf("MaxPeerLimit() = %d, want 20", st.MaxPeerLimit())
	}
	if st.InboundLimit() != int(float64(st.ConnectedPeerLimit())*InboundRatio) {
		t.Fatalf("InboundLimit() mismatch")
	}
	if st.IsAboveInboundLimit() {
		t.Fatal("expected not above inbound limit with no peers")
	}
}

func TestStatus_ScorersAccessor(t *testing.T) {
	st := newTestStatus(t, 10)
	if st.Scorers() == nil {
		t.Fatal("expected non-nil Scorers()")
	}
}

func TestStatus_ChainState(t *testing.T) {
	st := newTestStatus(t, 10)
	pid := randPeerID(t)
	st.Add(nil, pid, addr(t, "/ip4/1.2.3.4/tcp/1"), network.DirInbound)

	cs := &sync_pb.Status{}
	st.SetChainState(pid, cs)

	got, err := st.ChainState(pid)
	if err != nil {
		t.Fatalf("ChainState: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil ChainState after SetChainState")
	}

	lastUpdated, err := st.ChainStateLastUpdated(pid)
	if err != nil {
		t.Fatalf("ChainStateLastUpdated: %v", err)
	}
	if lastUpdated.IsZero() {
		t.Fatal("expected non-zero ChainStateLastUpdated after SetChainState")
	}
}
