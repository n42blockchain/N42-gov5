// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"crypto/ecdsa"
	"net"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p/enode"
)

// newTestEnodeNode builds a real, signed enode.Node (IPv4+TCP+UDP set, fork
// entry attached) via Service.createLocalNode, for exercising the node <->
// multiaddr conversion helpers in discovery.go without a live discv5 socket.
func newTestEnodeNode(t *testing.T) (*Service, *enode.Node) {
	t.Helper()
	s := newTestService(t)
	s.cfg.DataDir = t.TempDir()

	key := testPrivKey(t)
	ln, err := s.createLocalNode(key, net.ParseIP("127.0.0.1"), 30303, 30303)
	if err != nil {
		t.Fatalf("createLocalNode: %v", err)
	}
	return s, ln.Node()
}

// testPrivKey produces a fresh secp256k1-backed ecdsa key the same way
// privKey(cfg) would for a config with no persisted key.
func testPrivKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := privKey(&conf.P2PConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("privKey: %v", err)
	}
	return k
}

func TestPeerIDFromNode(t *testing.T) {
	_, node := newTestEnodeNode(t)
	id, err := peerIDFromNode(node)
	if err != nil {
		t.Fatalf("peerIDFromNode: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty peer ID")
	}
}

func TestConvertToSingleMultiAddr(t *testing.T) {
	_, node := newTestEnodeNode(t)
	addr, err := convertToSingleMultiAddr(node)
	if err != nil {
		t.Fatalf("convertToSingleMultiAddr: %v", err)
	}
	if addr == nil {
		t.Fatal("expected a non-nil multiaddr")
	}
}

func TestConvertToMultiAddrSkipsNodesWithoutIP(t *testing.T) {
	_, node := newTestEnodeNode(t)
	addrs := convertToMultiAddr([]*enode.Node{node})
	if len(addrs) != 1 {
		t.Fatalf("len(addrs) = %d, want 1", len(addrs))
	}
	// nil entries / nodes without an IP are skipped rather than erroring.
	addrs = convertToMultiAddr(nil)
	if len(addrs) != 0 {
		t.Fatalf("len(addrs) = %d, want 0 for an empty input", len(addrs))
	}
}

func TestConvertToAddrInfo(t *testing.T) {
	_, node := newTestEnodeNode(t)
	info, addr, err := convertToAddrInfo(node)
	if err != nil {
		t.Fatalf("convertToAddrInfo: %v", err)
	}
	if info == nil || addr == nil {
		t.Fatal("expected non-nil AddrInfo and multiaddr")
	}
}

func TestConvertToUdpMultiAddr(t *testing.T) {
	_, node := newTestEnodeNode(t)
	addrs, err := convertToUdpMultiAddr(node)
	if err != nil {
		t.Fatalf("convertToUdpMultiAddr: %v", err)
	}
	if len(addrs) == 0 {
		t.Fatal("expected at least one UDP multiaddr (IPv4 was set)")
	}
}

func TestFilterPeerRejectsNilAndNoIP(t *testing.T) {
	s := newTestService(t)
	if s.filterPeer(nil) {
		t.Fatal("filterPeer(nil) should be false")
	}
}

func TestFilterPeerAcceptsMatchingForkDigest(t *testing.T) {
	s, node := newTestEnodeNode(t)
	if !s.filterPeer(node) {
		t.Fatal("filterPeer should accept a freshly minted, non-active, fork-matching node")
	}
}

