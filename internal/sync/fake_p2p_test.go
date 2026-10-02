package sync

import (
	"context"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/test"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/p2p/peers"
	"github.com/n42blockchain/N42/internal/p2p/peers/scorers"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// fakeP2P is a lightweight p2p.P2P stand-in for RPC handler tests. It embeds
// the interface (nil) so any method we do not override panics loudly if a
// test accidentally reaches it, and overrides only what the sync package's
// RPC handlers actually use: peer status, encoding, host peerstore, and
// ping/sender plumbing.
type fakeP2P struct {
	p2p.P2P
	peerStatus *peers.Status
	self       peer.ID
	ping       *sync_pb.Ping

	sendStream network.Stream
	sendErr    error

	disconnected []peer.ID

	// realHost/realPubSub back the P2P methods that return concrete libp2p
	// types the sync package cannot otherwise fake: SetStreamHandler, Host,
	// PubSub, SubscribeToTopic, LeaveTopic. Built lazily (newFakeP2PWithHost)
	// only by tests that exercise those paths, since spinning up an
	// in-process host/gossipsub pair is unnecessary overhead for most RPC
	// handler tests.
	realHost    host.Host
	realPubSub  *pubsub.PubSub
	topicsMu    sync.Mutex
	joinedTopic map[string]*pubsub.Topic
}

// newFakeP2P returns a fakeP2P with a real *peers.Status (newTestStatus-style
// construction, mirroring internal/p2p/peers/status_test.go) and no peers
// connected yet.
func newFakeP2P(t *testing.T) *fakeP2P {
	t.Helper()
	st := peers.NewStatus(context.Background(), &peers.StatusConfig{
		PeerLimit:    32,
		ScorerParams: &scorers.Config{},
	})
	self, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	return &fakeP2P{
		peerStatus: st,
		self:       self,
		ping:       &sync_pb.Ping{SeqNumber: 0},
	}
}

func (f *fakeP2P) Peers() *peers.Status            { return f.peerStatus }
func (f *fakeP2P) PeerID() peer.ID                 { return f.self }
func (f *fakeP2P) Encoding() encoder.NetworkEncoding { return encoder.SszNetworkEncoder{} }
func (f *fakeP2P) GetConfig() *conf.P2PConfig      { return &conf.P2PConfig{} }
func (f *fakeP2P) GetPing() *sync_pb.Ping          { return f.ping }
func (f *fakeP2P) IncSeqNumber()                   { f.ping.SeqNumber++ }

func (f *fakeP2P) Disconnect(id peer.ID) error {
	f.disconnected = append(f.disconnected, id)
	f.peerStatus.SetConnectionState(id, peers.PeerDisconnected)
	return nil
}

func (f *fakeP2P) Send(_ context.Context, _ interface{}, _ string, _ peer.ID) (network.Stream, error) {
	return f.sendStream, f.sendErr
}

// newFakeP2PWithHost returns a fakeP2P backed by a real in-process libp2p
// host (no listen addresses, no sockets) and a real GossipSub router, so
// SetStreamHandler/Host/PubSub/SubscribeToTopic/LeaveTopic all behave like
// the production p2p.Service for tests that construct or tear down a full
// Service (NewService/Start/Stop/RegisterHandlers, registerSubscribers).
func newFakeP2PWithHost(t *testing.T) *fakeP2P {
	t.Helper()
	f := newFakeP2P(t)
	h, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	f.self = h.ID()
	f.realHost = h

	gs, err := pubsub.NewGossipSub(context.Background(), h)
	if err != nil {
		t.Fatalf("pubsub.NewGossipSub: %v", err)
	}
	f.realPubSub = gs
	f.joinedTopic = make(map[string]*pubsub.Topic)
	return f
}

func (f *fakeP2P) Host() host.Host { return f.realHost }

func (f *fakeP2P) PubSub() *pubsub.PubSub { return f.realPubSub }

func (f *fakeP2P) SetStreamHandler(topic string, handler network.StreamHandler) {
	f.realHost.SetStreamHandler(protocol.ID(topic), handler)
}

func (f *fakeP2P) joinTopic(topic string) (*pubsub.Topic, error) {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	if th, ok := f.joinedTopic[topic]; ok {
		return th, nil
	}
	th, err := f.realPubSub.Join(topic)
	if err != nil {
		return nil, err
	}
	f.joinedTopic[topic] = th
	return th, nil
}

func (f *fakeP2P) SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	th, err := f.joinTopic(topic)
	if err != nil {
		return nil, err
	}
	return th.Subscribe(opts...)
}

func (f *fakeP2P) AddConnectionHandler(_ func(ctx context.Context, id peer.ID) error,
	_ func(ctx context.Context, id peer.ID) error) {
}

func (f *fakeP2P) AddDisconnectionHandler(_ func(ctx context.Context, id peer.ID) error) {}

func (f *fakeP2P) AddPingMethod(_ func(ctx context.Context, id peer.ID) error) {}

func (f *fakeP2P) LeaveTopic(topic string) error {
	f.topicsMu.Lock()
	defer f.topicsMu.Unlock()
	th, ok := f.joinedTopic[topic]
	if !ok {
		return nil
	}
	if err := th.Close(); err != nil {
		return err
	}
	delete(f.joinedTopic, topic)
	return nil
}

// addConnectedPeer registers pid as a connected peer at the given height, so
// BestPeers/Connected/HeightBehind-style logic can see it.
func (f *fakeP2P) addConnectedPeer(t *testing.T, pid peer.ID, height uint64) {
	t.Helper()
	a, err := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/30303")
	if err != nil {
		t.Fatalf("NewMultiaddr: %v", err)
	}
	f.peerStatus.Add(nil, pid, a, network.DirOutbound)
	f.peerStatus.SetConnectionState(pid, peers.PeerConnected)
	f.peerStatus.SetChainState(pid, &sync_pb.Status{
		CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(height)),
	})
}

var _ p2p.P2P = (*fakeP2P)(nil)
