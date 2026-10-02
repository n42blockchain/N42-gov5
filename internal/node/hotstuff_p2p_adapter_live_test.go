package node

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
)

// nilHostP2P embeds a nil p2p.P2P and overrides only Host() to return nil
// without going through the (nil, thus panicking) embedded interface. The
// adapter methods under test here call nothing else on the interface, so no
// other method needs a real implementation.
type nilHostP2P struct {
	p2p.P2P
}

func (nilHostP2P) Host() host.Host { return nil }

// hotstuffAdapterLiveServices builds two real, loopback-only p2p.Service
// instances (LocalIP 127.0.0.1, TCPPort 0, discovery and static peers off)
// and connects them directly via libp2p host.Connect, bypassing discovery
// entirely. Both are wrapped in a *hotstuffP2PAdapter so SendRawBytes,
// SetStreamHandler and ConnectedPeers can be exercised against a live,
// connected pair rather than mocks.
func hotstuffAdapterLiveServices(t *testing.T) (a, b *hotstuffP2PAdapter) {
	t.Helper()
	ctx := context.Background()

	newSvc := func(dir string) *p2p.Service {
		cfg := &conf.P2PConfig{
			DataDir:      dir,
			LocalIP:      "127.0.0.1",
			TCPPort:      0,
			MaxPeers:     8,
			MinSyncPeers: 0,
			NoDiscovery:  true,
		}
		svc, err := p2p.NewService(ctx, types.Hash{}, cfg, conf.NodeConfig{DataDir: dir})
		if err != nil {
			t.Fatalf("p2p.NewService: %v", err)
		}
		return svc
	}

	svcA := newSvc(t.TempDir())
	svcB := newSvc(t.TempDir())
	svcA.Start()
	svcB.Start()
	t.Cleanup(func() { _ = svcA.Stop() })
	t.Cleanup(func() { _ = svcB.Stop() })

	bAddrInfo := peer.AddrInfo{ID: svcB.Host().ID(), Addrs: svcB.Host().Addrs()}
	if err := svcA.Host().Connect(ctx, bAddrInfo); err != nil {
		t.Fatalf("connect A->B: %v", err)
	}

	return newHotstuffP2PAdapter(svcA), newHotstuffP2PAdapter(svcB)
}

// TestHotstuffP2PAdapterSendRawBytesDeliversToHandler exercises
// SetStreamHandler's registration and SendRawBytes's stream-open/write/close
// path end to end over a real connected libp2p pair.
func TestHotstuffP2PAdapterSendRawBytesDeliversToHandler(t *testing.T) {
	a, b := hotstuffAdapterLiveServices(t)

	const topic = "/n42/test/hotstuff-adapter/1.0.0"
	received := make(chan []byte, 1)
	b.SetStreamHandler(topic, func(data []byte, from peer.ID) {
		received <- data
	})

	payload := []byte("hotstuff direct-send payload")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.SendRawBytes(ctx, payload, topic, b.Host().ID()); err != nil {
		t.Fatalf("SendRawBytes: %v", err)
	}

	select {
	case got := <-received:
		if string(got) != string(payload) {
			t.Fatalf("handler received %q, want %q", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not receive the message in time")
	}
}

// TestHotstuffP2PAdapterSendRawBytesNoHostFails exercises the "host
// unavailable" error branch of SendRawBytes when the underlying p2p.P2P
// implementation's Host() returns nil.
func TestHotstuffP2PAdapterSendRawBytesNoHostFails(t *testing.T) {
	a := newHotstuffP2PAdapter(nilHostP2P{})
	err := a.SendRawBytes(context.Background(), []byte("x"), "/topic/1.0.0", peer.ID(""))
	if err == nil {
		t.Fatal("expected an error when the host is unavailable")
	}
}

// TestHotstuffP2PAdapterSetStreamHandlerNoHostIsNoop exercises
// SetStreamHandler's early return when Host() is nil: it must not panic.
func TestHotstuffP2PAdapterSetStreamHandlerNoHostIsNoop(t *testing.T) {
	a := newHotstuffP2PAdapter(nilHostP2P{})
	a.SetStreamHandler("/topic/1.0.0", func(data []byte, from peer.ID) {})
}

// TestHotstuffP2PAdapterConnectedPeersNoHostReturnsNil exercises
// ConnectedPeers's early return when Host() is nil.
func TestHotstuffP2PAdapterConnectedPeersNoHostReturnsNil(t *testing.T) {
	a := newHotstuffP2PAdapter(nilHostP2P{})
	if peers := a.ConnectedPeers(); peers != nil {
		t.Fatalf("ConnectedPeers() = %v, want nil", peers)
	}
}

// TestHotstuffP2PAdapterConnectedPeersReportsLivePeer exercises
// ConnectedPeers's success path against a real connected pair.
func TestHotstuffP2PAdapterConnectedPeersReportsLivePeer(t *testing.T) {
	a, b := hotstuffAdapterLiveServices(t)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(a.ConnectedPeers()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	peers := a.ConnectedPeers()
	if len(peers) == 0 {
		t.Fatal("expected ConnectedPeers to report at least one connected peer")
	}
	found := false
	for _, p := range peers {
		if p == b.Host().ID() {
			found = true
		}
	}
	if !found {
		t.Fatalf("ConnectedPeers() = %v, want to include %s", peers, b.Host().ID())
	}
}
