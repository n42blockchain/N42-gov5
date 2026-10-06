// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"crypto/rand"
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/n42blockchain/N42/proto/sync_pb"
)

func TestServiceAccessors(t *testing.T) {
	s := newTestService(t)

	if s.Encoding() == nil {
		t.Fatal("Encoding() returned nil")
	}
	if s.PubSub() == nil {
		t.Fatal("PubSub() returned nil")
	}
	if s.Host() == nil {
		t.Fatal("Host() returned nil")
	}
	if s.PeerID() != s.host.ID() {
		t.Fatal("PeerID() mismatch")
	}
	if s.Peers() == nil {
		t.Fatal("Peers() returned nil")
	}
	if got := s.GetConfig(); got != s.cfg {
		t.Fatal("GetConfig() mismatch")
	}
	if s.ENR() != nil {
		t.Fatal("ENR() should be nil without a discovery listener")
	}
	addrs, err := s.DiscoveryAddresses()
	if err != nil || addrs != nil {
		t.Fatalf("DiscoveryAddresses() = %v, %v; want nil, nil", addrs, err)
	}
}

func TestServiceGetPingAndIncSeqNumber(t *testing.T) {
	s := newTestService(t)
	s.ping = &sync_pb.Ping{SeqNumber: 5}

	if got := s.GetPing().SeqNumber; got != 5 {
		t.Fatalf("GetPing().SeqNumber = %d, want 5", got)
	}
	s.IncSeqNumber()
	if got := s.GetPing().SeqNumber; got != 6 {
		t.Fatalf("after IncSeqNumber, SeqNumber = %d, want 6", got)
	}
}

func TestServiceStartedAndStatus(t *testing.T) {
	s := newTestService(t)

	if s.Started() {
		t.Fatal("Started() should be false before Start")
	}
	// isPreGenesis is true by default in newTestService: Status must be nil.
	if err := s.Status(); err != nil {
		t.Fatalf("Status() in pre-genesis = %v, want nil", err)
	}

	s.isPreGenesis.Store(false)
	if err := s.Status(); err == nil {
		t.Fatal("Status() should report an error when not started and not pre-genesis")
	}

	s.started.Store(true)
	if err := s.Status(); err != nil {
		t.Fatalf("Status() when started = %v, want nil", err)
	}

	s.recordStartupError(errors.New("boom"))
	if s.Started() {
		t.Fatal("recordStartupError should stop the service")
	}
	// Status() checks started() before startupErr, so once stopped it reports
	// "not running" rather than the recorded error — that error is only
	// surfaced while started() is still true, which recordStartupError
	// itself prevents. Confirm the stored error directly instead.
	if err := s.Status(); err == nil {
		t.Fatal("Status() after recordStartupError should report an error")
	}
	s.startupErrMu.RLock()
	stored := s.startupErr
	s.startupErrMu.RUnlock()
	if stored == nil || stored.Error() != "boom" {
		t.Fatalf("stored startupErr = %v, want boom", stored)
	}
}

func TestServiceSetStreamHandler(t *testing.T) {
	s := newTestService(t)
	s.SetStreamHandler("/n42/test/1.0.0", func(stream network.Stream) {
		_ = stream.Close()
	})
	// Exercising the handler itself requires a connected peer; covered
	// indirectly by the mocknet-based pubsub test in pubsub_mocknet_test.go.
}

func TestServiceConnectionGaterMethods(t *testing.T) {
	s := newTestService(t)

	pid := s.host.ID()
	if !s.InterceptPeerDial(pid) {
		t.Fatal("InterceptPeerDial on an unknown-but-not-bad peer should allow")
	}

	other := randomPeerID(t)
	for i := 0; i < 10; i++ {
		s.peers.Scorers().BadResponsesScorer().Increment(other)
	}
	if s.InterceptPeerDial(other) {
		t.Fatal("InterceptPeerDial should reject a bad peer")
	}
	s.protectedPeers[other] = struct{}{}
	if !s.InterceptPeerDial(other) {
		t.Fatal("InterceptPeerDial should allow a protected bad peer")
	}
	if !s.isProtectedPeer(other) {
		t.Fatal("isProtectedPeer should report true for a protected peer")
	}
	delete(s.protectedPeers, other)
	if s.isProtectedPeer(other) {
		t.Fatal("isProtectedPeer should report false once unprotected")
	}

	loopback := mustMultiaddr(t, "/ip4/127.0.0.1/tcp/4001")
	fakeConn := fakeConnMultiaddrs{remote: loopback}

	// InterceptAccept rejects everything before the service is started.
	if s.InterceptAccept(fakeConn) {
		t.Fatal("InterceptAccept should reject before Start")
	}
	s.started.Store(true)
	if !s.InterceptAccept(fakeConn) {
		t.Fatal("InterceptAccept should allow a loopback dial once started, under the peer limit")
	}

	if allow, reason := s.InterceptUpgraded(nil); !allow || reason != 0 {
		t.Fatalf("InterceptUpgraded = %v, %v; want true, 0", allow, reason)
	}

	if !s.InterceptSecured(network.DirOutbound, pid, fakeConn) {
		t.Fatal("InterceptSecured should allow an outbound, non-bad peer")
	}
	if !s.InterceptAddrDial(pid, loopback) {
		t.Fatal("InterceptAddrDial should allow a non-bad peer with no filter configured")
	}

	for i := 0; i < 10; i++ {
		s.peers.Scorers().BadResponsesScorer().Increment(other)
	}
	if s.InterceptAddrDial(other, loopback) {
		t.Fatal("InterceptAddrDial should reject a bad peer")
	}
	if s.InterceptSecured(network.DirOutbound, other, fakeConn) {
		t.Fatal("InterceptSecured should reject a bad peer")
	}
}

// randomPeerID produces a fresh, valid peer ID unrelated to any live host.
func randomPeerID(t *testing.T) peer.ID {
	t.Helper()
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	return pid
}

func mustMultiaddr(t *testing.T, s string) ma.Multiaddr {
	t.Helper()
	m, err := ma.NewMultiaddr(s)
	if err != nil {
		t.Fatalf("NewMultiaddr(%q): %v", s, err)
	}
	return m
}

type fakeConnMultiaddrs struct {
	remote ma.Multiaddr
}

func (f fakeConnMultiaddrs) LocalMultiaddr() ma.Multiaddr  { return f.remote }
func (f fakeConnMultiaddrs) RemoteMultiaddr() ma.Multiaddr { return f.remote }
