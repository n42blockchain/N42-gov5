package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

func performanceChainFixture() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID: big.NewInt(params.PerformanceChainID), Consensus: params.HotStuffConsensus,
		StateScheme: "qmdb", HotStuff: &params.HotStuffConfig{},
		Performance: &params.PerformanceChainConfig{
			Version: 1, Mode: params.PerformanceModeIndependent, Role: params.PerformanceRoleLocal,
			Transactions: params.PerformanceTransactionsETH, NetworkNamespace: params.PerformanceNamespace,
		},
	}
}

func performanceNetworkKey(t *testing.T) (string, peer.ID) {
	t.Helper()
	key, _, err := libcrypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := key.Raw()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte(hex.EncodeToString(raw)), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return file, id
}

func performanceStaticPeer(id peer.ID) string {
	return "/ip4/127.0.0.1/tcp/1/p2p/" + id.String()
}

func performanceSocketService(t *testing.T, key string, peers []peer.ID, genesis types.Hash, enabled bool) *Service {
	t.Helper()
	cfg := &conf.P2PConfig{PrivateKey: key, DataDir: t.TempDir(), TCPPort: 0, MaxPeers: 30, LocalIP: "127.0.0.1", QUICEnabled: true}
	for _, id := range peers {
		cfg.StaticPeers = append(cfg.StaticPeers, performanceStaticPeer(id))
	}
	var chainCfg *params.ChainConfig
	if enabled {
		chainCfg = performanceChainFixture()
	}
	s, err := NewService(context.Background(), genesis, cfg, conf.NodeConfig{Chain: "private", Profile: "n42"}, WithPerformanceChain(chainCfg))
	if err != nil {
		t.Fatal(err)
	}
	// Only the host participates in these bounded loopback tests. No fleet,
	// discovery, node databases, startup watchers or consensus are started.
	s.started.Store(true)
	t.Cleanup(func() { _ = s.Stop(); _ = s.Host().Close() })
	return s
}

func TestPerformanceP2PDefaultsAndUnsafeConfiguration(t *testing.T) {
	source := &conf.P2PConfig{QUICEnabled: true}
	configured, _, err := performanceP2PConfig(source)
	if err != nil || !configured.NoDiscovery || configured.QUICEnabled || configured.LocalIP != "127.0.0.1" {
		t.Fatalf("unsafe performance defaults: %+v %v", configured, err)
	}
	if source.NoDiscovery || !source.QUICEnabled || source.LocalIP != "" {
		t.Fatal("mutated shared/default P2P config")
	}
	legacy := &Service{cfg: source}
	if err := WithPerformanceChain(nil)(legacy, conf.NodeConfig{}); err != nil || legacy.cfg != source || legacy.performance != nil {
		t.Fatal("nil performance changed legacy service")
	}
	_, id := performanceNetworkKey(t)
	for _, change := range []func(*conf.P2PConfig){
		func(c *conf.P2PConfig) { c.BootstrapNodeAddr = []string{"enode://public"} },
		func(c *conf.P2PConfig) { c.Discv5BootStrapAddr = []string{"enr:public"} },
		func(c *conf.P2PConfig) { c.RelayNodeAddr = "relay" },
		func(c *conf.P2PConfig) { c.EnableUPnP = true },
		func(c *conf.P2PConfig) { c.HostDNS = "localhost" },
		func(c *conf.P2PConfig) { c.LocalIP = "0.0.0.0" },
		func(c *conf.P2PConfig) { c.HostAddress = "203.0.113.1" },
		func(c *conf.P2PConfig) { c.LocalIP = "::1" },
		func(c *conf.P2PConfig) { c.StaticPeers = []string{"/ip6/::1/tcp/1/p2p/" + id.String()} },
		func(c *conf.P2PConfig) { c.StaticPeers = []string{"/ip4/203.0.113.1/tcp/1/p2p/" + id.String()} },
		func(c *conf.P2PConfig) { c.StaticPeers = []string{"/dns4/localhost/tcp/1/p2p/" + id.String()} },
	} {
		cfg := &conf.P2PConfig{}
		change(cfg)
		if err := ValidatePerformanceP2PConfig(cfg); err == nil {
			t.Fatal("unsafe performance P2P config accepted")
		}
	}
}

func TestPerformanceNoiseRejectsLegacyGenesisAndNonExplicitPeers(t *testing.T) {
	keyA, idA := performanceNetworkKey(t)
	keyB, idB := performanceNetworkKey(t)
	for _, tc := range []struct {
		name    string
		enabled bool
		hash    types.Hash
		allowed bool
		want    bool
	}{
		{"same-chain", true, types.Hash{1}, true, true},
		{"same-digest-different-genesis", true, types.Hash{0: 1, 31: 2}, true, false},
		{"legacy", false, types.Hash{1}, true, false},
		{"not-explicit", true, types.Hash{1}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowedA := []peer.ID{idB}
			if !tc.allowed {
				allowedA = nil
			}
			a := performanceSocketService(t, keyA, allowedA, types.Hash{1}, true)
			b := performanceSocketService(t, keyB, []peer.ID{idA}, tc.hash, tc.enabled)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := a.Host().Connect(ctx, peer.AddrInfo{ID: b.PeerID(), Addrs: b.Host().Addrs()})
			if (err == nil) != tc.want {
				t.Fatalf("connection result: %v, want success=%v", err, tc.want)
			}
			if tc.want {
				for _, c := range a.Host().Network().ConnsToPeer(b.PeerID()) {
					if string(c.ConnState().Security) != a.performance.prefix+"/noise" {
						t.Fatal("connection bypassed performance Noise")
					}
				}
			}
			// Reverse direction exercises inbound secured gating too, without
			// static-peer score protection bypassing the performance allowlist.
			if !tc.want {
				if err := b.Host().Connect(ctx, peer.AddrInfo{ID: a.PeerID(), Addrs: a.Host().Addrs()}); err == nil {
					t.Fatal("reverse connection bypassed isolation")
				}
			}
		})
	}
}

func TestPerformanceRPCGossipAndHandlerRemoval(t *testing.T) {
	keyA, idA := performanceNetworkKey(t)
	keyB, idB := performanceNetworkKey(t)
	a := performanceSocketService(t, keyA, []peer.ID{idB}, types.Hash{1}, true)
	b := performanceSocketService(t, keyB, []peer.ID{idA}, types.Hash{1}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Host().Connect(ctx, peer.AddrInfo{ID: b.PeerID(), Addrs: b.Host().Addrs()}); err != nil {
		t.Fatal(err)
	}
	const logicalRPC = "/rpc/performance_test/1"
	b.Host().SetStreamHandlerMatch(protocol.ID(logicalRPC), func(id protocol.ID) bool { return strings.HasPrefix(string(id), logicalRPC) }, func(stream network.Stream) {
		defer stream.Close()
		if string(stream.Protocol()) != logicalRPC {
			_ = stream.Reset()
			return
		}
		_, _ = io.WriteString(stream, "isolated")
	})
	stream, err := a.Host().NewStream(ctx, b.PeerID(), protocol.ID(logicalRPC))
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil || string(got) != "isolated" {
		t.Fatalf("scoped RPC failed: %q %v", got, err)
	}
	rawHost := a.Host().(*scopedHost).Host
	if unscoped, err := rawHost.NewStream(ctx, b.PeerID(), protocol.ID(logicalRPC)); err == nil {
		_ = unscoped.Reset()
		t.Fatal("unscoped RPC alias was registered")
	}
	b.Host().RemoveStreamHandler(protocol.ID(logicalRPC))
	// Libp2p may optimistically return a stream for a cached remote protocol
	// before renegotiation. Clear that client-side hint to test handler removal.
	if err := a.Host().Peerstore().RemoveProtocols(b.PeerID(), protocol.ID(a.WireTopic(logicalRPC))); err != nil {
		t.Fatal(err)
	}
	if removed, err := a.Host().NewStream(ctx, b.PeerID(), protocol.ID(logicalRPC)); err == nil {
		_ = removed.Reset()
		t.Fatal("scoped RPC handler was not removed")
	}
	const logicalTopic = "/n42/msg/shard/0"
	sub, err := b.SubscribeToTopic(logicalTopic)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	topic, err := a.JoinTopic(logicalTopic)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Topic() == logicalTopic || !b.CanSubscribe(sub.Topic()) || b.CanSubscribe(logicalTopic) {
		t.Fatal("gossip did not enforce namespace")
	}
	if b.CanSubscribe(strings.Replace(sub.Topic(), a.performance.prefix, "/n42-performance/foreign", 1)) {
		t.Fatal("foreign namespace accepted")
	}
	waitForTopicPeer(t, topic)
	if err := topic.Publish(ctx, []byte("scoped gossip")); err != nil {
		t.Fatal(err)
	}
	msg, err := sub.Next(ctx)
	if err != nil || string(msg.Data) != "scoped gossip" {
		t.Fatalf("gossip delivery failed: %v", err)
	}
	sub.Cancel()
	if err := b.LeaveTopic(sub.Topic()); err != nil {
		t.Fatal(err)
	}
	if len(b.PubSub().GetTopics()) != 0 {
		t.Fatal("wire topic was not removed")
	}
}
