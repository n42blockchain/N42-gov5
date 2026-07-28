package devp2p

import (
	"net"
	"sync/atomic"
	"testing"

	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func testTrustedNode(t *testing.T, port int) *enode.Node {
	t.Helper()
	key, err := gethcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return enode.NewV4(&key.PublicKey, net.IPv4(127, 0, 0, 1), port, port)
}

func TestTrustedPeerReferenceLifecycle(t *testing.T) {
	h := &EthHandler{}
	var marked, unmarked atomic.Int32
	h.setTrustedCallbacks(
		func(*enode.Node) { marked.Add(1) },
		func(*enode.Node) { unmarked.Add(1) },
	)
	node := testTrustedNode(t, 30303)

	if !h.markPeerTrusted(node) || !h.markPeerTrusted(node) {
		t.Fatal("duplicate connections should both acquire a trusted reference")
	}
	if got := marked.Load(); got != 1 {
		t.Fatalf("mark callback count = %d, want 1", got)
	}
	h.unmarkPeerTrusted(node)
	if got := unmarked.Load(); got != 0 {
		t.Fatalf("peer unmarked while one connection remains: %d", got)
	}
	h.unmarkPeerTrusted(node)
	if got := unmarked.Load(); got != 1 {
		t.Fatalf("unmark callback count = %d, want 1", got)
	}
	if len(h.trustedIDs) != 0 {
		t.Fatalf("trusted peer leaked after disconnect: %d", len(h.trustedIDs))
	}
}

func TestTrustedPeerCapacityIsReusable(t *testing.T) {
	h := &EthHandler{}
	h.setTrustedCallbacks(func(*enode.Node) {}, func(*enode.Node) {})
	nodes := make([]*enode.Node, maxTrustedPeers)
	for i := range nodes {
		nodes[i] = testTrustedNode(t, 31000+i)
		if !h.markPeerTrusted(nodes[i]) {
			t.Fatalf("peer %d rejected before capacity", i)
		}
	}
	extra := testTrustedNode(t, 32000)
	if h.markPeerTrusted(extra) {
		t.Fatal("peer admitted above trusted capacity")
	}

	h.unmarkPeerTrusted(nodes[0])
	if !h.markPeerTrusted(extra) {
		t.Fatal("freed trusted slot was not reusable")
	}
}
