package sync

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
)

// fakeConnectednessHost is a host.Host stand-in whose only meaningful
// behaviour is reporting a fixed Connectedness for Network().Connectedness,
// which is all sendGoodByeAndDisconnect inspects before sending.
type fakeConnectednessHost struct {
	host.Host
	state network.Connectedness
}

func (h *fakeConnectednessHost) Network() network.Network {
	return &fakeConnectednessNetwork{state: h.state}
}

type fakeConnectednessNetwork struct {
	network.Network
	state network.Connectedness
}

func (n *fakeConnectednessNetwork) Connectedness(peer.ID) network.Connectedness { return n.state }

// TestSendGoodByeAndDisconnectSkipsWhenNotConnected covers the early-return
// branch: a peer the host reports as not connected is never sent a goodbye.
func TestSendGoodByeAndDisconnectSkipsWhenNotConnected(t *testing.T) {
	svc, fp := newPingTestService(t)
	fp.realHost = &fakeConnectednessHost{state: network.NotConnected}
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}

	if err := svc.sendGoodByeAndDisconnect(context.Background(), p2ptypes.GoodbyeCodeGenericError, remote); err != nil {
		t.Fatalf("sendGoodByeAndDisconnect: %v", err)
	}
	if len(fp.disconnected) != 0 {
		t.Fatalf("expected no Disconnect call for a not-connected peer, got %v", fp.disconnected)
	}
}

// TestSendGoodByeAndDisconnectSendsAndDisconnects covers the connected path:
// sendGoodByeMessage sends over the (fake) stream, reads the ack, then
// Disconnect is called regardless of the send outcome.
func TestSendGoodByeAndDisconnectSendsAndDisconnects(t *testing.T) {
	svc, fp := newPingTestService(t)
	fp.realHost = &fakeConnectednessHost{state: network.Connected}
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}

	client, server := newFakeStreamPair(fp.self, remote)
	fp.sendStream = server
	go func() {
		// sendGoodByeMessage's stream comes pre-"sent" from fakeP2P.Send (no
		// actual write happens on this fake), so its blocking ack-read
		// ([]byte{0}) needs the client side to write something.
		_, _ = client.Write([]byte{0})
	}()

	if err := svc.sendGoodByeAndDisconnect(context.Background(), p2ptypes.GoodbyeCodeGenericError, remote); err != nil {
		t.Fatalf("sendGoodByeAndDisconnect: %v", err)
	}
	if len(fp.disconnected) != 1 || fp.disconnected[0] != remote {
		t.Fatalf("expected Disconnect(remote), got %v", fp.disconnected)
	}
}

// TestSendGoodByeAndDisconnectSendErrorStillDisconnects covers
// sendGoodByeMessage's own error path (p2p.Send fails): the error is only
// logged, and Disconnect still runs.
func TestSendGoodByeAndDisconnectSendErrorStillDisconnects(t *testing.T) {
	svc, fp := newPingTestService(t)
	fp.realHost = &fakeConnectednessHost{state: network.Connected}
	fp.sendErr = context.DeadlineExceeded
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}

	if err := svc.sendGoodByeAndDisconnect(context.Background(), p2ptypes.GoodbyeCodeGenericError, remote); err != nil {
		t.Fatalf("sendGoodByeAndDisconnect: %v", err)
	}
	if len(fp.disconnected) != 1 {
		t.Fatalf("expected Disconnect(remote) even on send error, got %v", fp.disconnected)
	}
}

// TestSendGoodbyeWrapsGenericError covers the one-line sendGoodbye wrapper.
func TestSendGoodbyeWrapsGenericError(t *testing.T) {
	svc, fp := newPingTestService(t)
	fp.realHost = &fakeConnectednessHost{state: network.NotConnected}
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	if err := svc.sendGoodbye(context.Background(), remote); err != nil {
		t.Fatalf("sendGoodbye: %v", err)
	}
}
