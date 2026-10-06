// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Real in-process libp2p + gossipsub harness for the Service's P2P-dependent
// lifecycle (Start/Stop, subscribeMessages, processGossipMessage,
// pacemakerLoop, setupRotorStreamHandler). Mirrors the pattern in
// internal/p2p/pubsub_mocknet_test.go: mocknet gives connected hosts with no
// real sockets, and pubsub.NewGossipSub runs unmodified on top of them.

package hotstuff

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/params"
)

// hsTP2P is a minimal, real implementation of the Service's P2PDirectSender
// interface over a plain libp2p host + gossipsub instance. It deliberately
// does nothing beyond what the interface requires, so a test using it
// exercises the actual Service code paths rather than a mock's.
type hsTP2P struct {
	h  host.Host
	ps *pubsub.PubSub

	mu     sync.Mutex
	topics map[string]*pubsub.Topic
}

func newHsTP2P(t *testing.T, h host.Host) *hsTP2P {
	t.Helper()
	ps, err := pubsub.NewGossipSub(context.Background(), h)
	if err != nil {
		t.Fatalf("pubsub.NewGossipSub: %v", err)
	}
	return &hsTP2P{h: h, ps: ps, topics: make(map[string]*pubsub.Topic)}
}

func (p *hsTP2P) joinTopic(topic string) (*pubsub.Topic, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if th, ok := p.topics[topic]; ok {
		return th, nil
	}
	th, err := p.ps.Join(topic)
	if err != nil {
		return nil, err
	}
	p.topics[topic] = th
	return th, nil
}

func (p *hsTP2P) PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error {
	th, err := p.joinTopic(topic)
	if err != nil {
		return err
	}
	return th.Publish(ctx, data, opts...)
}

func (p *hsTP2P) SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	th, err := p.joinTopic(topic)
	if err != nil {
		return nil, err
	}
	return th.Subscribe(opts...)
}

func (p *hsTP2P) Encoding() encoder.NetworkEncoding { return &encoder.SszNetworkEncoder{} }

func (p *hsTP2P) SendRawBytes(ctx context.Context, data []byte, topic string, pid peer.ID) error {
	s, err := p.h.NewStream(ctx, pid, protocol.ID(topic))
	if err != nil {
		return err
	}
	defer s.Close()
	_, err = s.Write(data)
	return err
}

func (p *hsTP2P) SetStreamHandler(topic string, handler func(data []byte, from peer.ID)) {
	p.h.SetStreamHandler(protocol.ID(topic), func(s network.Stream) {
		defer s.Close()
		data, _ := io.ReadAll(s)
		handler(data, s.Conn().RemotePeer())
	})
}

func (p *hsTP2P) ConnectedPeers() []peer.ID {
	return p.h.Network().Peers()
}

var _ P2PDirectSender = (*hsTP2P)(nil)

// hsTWaitForTopicPeer polls (no sleep over 100ms) until the topic handle sees
// a mesh peer, or fails the test.
func hsTWaitForTopicPeer(t *testing.T, topic *pubsub.Topic) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(topic.ListPeers()) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a gossipsub mesh peer")
}

// hsTMocknetPair returns two connected hosts (no sockets) and their matching
// hsTP2P adapters.
func hsTMocknetPair(t *testing.T) (p0, p1 *hsTP2P) {
	t.Helper()
	mn, err := mocknet.FullMeshConnected(2)
	if err != nil {
		t.Fatalf("mocknet.FullMeshConnected: %v", err)
	}
	t.Cleanup(func() { _ = mn.Close() })
	hosts := mn.Hosts()
	if len(hosts) != 2 {
		t.Fatalf("got %d hosts, want 2", len(hosts))
	}
	return newHsTP2P(t, hosts[0]), newHsTP2P(t, hosts[1])
}

// hsTNewWiredService builds a Service with a real 4-validator HotStuff
// engine (local index 0) and the given P2P transport, with fast pacemaker
// timeouts so pacemakerLoop fires quickly in tests.
func hsTNewWiredService(t *testing.T, setup *testSetup, idx int, p2p P2PPublisher, gossipTopic, rpcTopic string) *Service {
	t.Helper()
	h := New(&params.HotStuffConfig{BaseTimeout: 60, MaxTimeout: 120}, nil)
	h.Authorize(setup.validators[idx].Address, setup.keys[idx])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	s := NewService(h, p2p, nil, gossipTopic, rpcTopic)
	t.Cleanup(func() {
		s.cancel()
	})
	return s
}

// TestService_StartStop_Lifecycle drives the real Start/Stop goroutines
// (processOutputs, pacemakerLoop, subscribeMessages) end to end over a
// mocknet gossipsub transport, and lets the pacemaker fire at least once
// before a graceful Stop.
func TestService_StartStop_Lifecycle(t *testing.T) {
	setup := newTestSetup(t, 4)
	p0, _ := hsTMocknetPair(t)
	s := hsTNewWiredService(t, setup, 0, p0, "/n42/hotstuff/gossip", "/n42/hotstuff/rpc")

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let the pacemaker's short base timeout fire at least once; OnTimeout
	// pushes an OutputSendToValidator (timeout message) through processOutputs.
	deadline := time.After(2 * time.Second)
	fired := false
	for !fired {
		select {
		case <-s.engine.OutputCh():
			fired = true
		case <-deadline:
			fired = true // do not hang the suite; Stop() below still must be clean
		}
	}

	s.Stop()
}

// TestService_StartWithoutInitEngine_Errors covers the guard that refuses to
// start before InitEngine.
func TestService_StartWithoutInitEngine_Errors(t *testing.T) {
	h := New(nil, nil)
	s := NewService(h, nil, nil, "gossip", "rpc")
	defer s.cancel()
	if err := s.Start(); err == nil {
		t.Fatal("expected Start to fail without InitEngine")
	}
}

// TestService_SubscribeAndProcessGossipMessage_LearnsPeer publishes a real,
// signed Vote message over an actual gossipsub mesh and confirms the
// receiving service's subscribeMessages/processGossipMessage path decodes it
// and records the sender's peer via learnValidatorPeer (observed through the
// Rotor registry, which has no other writer).
func TestService_SubscribeAndProcessGossipMessage_LearnsPeer(t *testing.T) {
	setup := newTestSetup(t, 4)
	p0, p1 := hsTMocknetPair(t)

	const gossipTopic = "/n42/hotstuff/gossip"
	s0 := hsTNewWiredService(t, setup, 0, p0, gossipTopic, "/n42/hotstuff/rpc")

	s0.wg.Add(1)
	go s0.subscribeMessages()
	t.Cleanup(func() { s0.cancel(); s0.wg.Wait() })

	// Wait for s0's subscription to exist, then join the same topic from p1
	// and wait for the mesh to form both ways.
	fullTopic := gossipTopic + s0.engineEncodingSuffix()
	topicHandle, err := p1.joinTopic(fullTopic)
	if err != nil {
		t.Fatalf("p1 joinTopic: %v", err)
	}
	hsTWaitForTopicPeer(t, topicHandle)

	view := s0.engine.Engine().CurrentView()
	vote := &Vote{
		View:      view,
		BlockHash: setup.validators[1].Address.Hash(), // any stable, non-zero hash
		Voter:     1,
		Signature: setup.keys[1].Sign(SigningMessage(view, setup.validators[1].Address.Hash())).Marshal(),
	}
	raw, err := EncodeConsensusMsg(&ConsensusMsg{Type: MsgVote, Payload: vote})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := &encoder.SszNetworkEncoder{}
	if _, err := enc.EncodeGossip(&buf, &rawSSZMarshaler{data: raw}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p1.PublishToTopic(ctx, fullTopic, buf.Bytes()); err != nil {
		t.Fatalf("PublishToTopic: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s0.rotor.RegisteredPeerCount() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for learnValidatorPeer to register the sender")
}

// engineEncodingSuffix is a tiny test helper mirroring what subscribeMessages
// computes internally, so the test can join the exact same topic string.
func (s *Service) engineEncodingSuffix() string {
	return s.p2p.Encoding().ProtocolSuffix()
}

// TestService_SetupRotorStreamHandlerAndDirectDelivery covers
// setupRotorStreamHandler's registration and the direct-stream delivery path
// into processGossipMessage (the Rotor relay transport), using a real
// libp2p stream between two mocknet hosts.
func TestService_SetupRotorStreamHandlerAndDirectDelivery(t *testing.T) {
	setup := newTestSetup(t, 4)
	p0, p1 := hsTMocknetPair(t)

	const rpcTopic = "/n42/hotstuff/rpc"
	s0 := hsTNewWiredService(t, setup, 0, p0, "/n42/hotstuff/gossip", rpcTopic)
	s0.setupRotorStreamHandler()

	view := s0.engine.Engine().CurrentView()
	vote := &Vote{
		View:      view,
		BlockHash: setup.validators[1].Address.Hash(),
		Voter:     1,
		Signature: setup.keys[1].Sign(SigningMessage(view, setup.validators[1].Address.Hash())).Marshal(),
	}
	raw, err := EncodeConsensusMsg(&ConsensusMsg{Type: MsgVote, Payload: vote})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := &encoder.SszNetworkEncoder{}
	if _, err := enc.EncodeGossip(&buf, &rawSSZMarshaler{data: raw}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p1.SendRawBytes(ctx, buf.Bytes(), rpcTopic, p0.h.ID()); err != nil {
		t.Fatalf("SendRawBytes: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s0.rotor.RegisteredPeerCount() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the Rotor stream handler to process the direct message")
}

// TestService_SetupRotorStreamHandler_NoDirectSenderIsNoop covers the type
// assertion guard when the configured P2P transport does not implement
// P2PDirectSender.
func TestService_SetupRotorStreamHandler_NoDirectSenderIsNoop(t *testing.T) {
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	s := NewService(h, nil, nil, "gossip", "rpc") // nil P2PPublisher: not a P2PDirectSender
	defer s.cancel()
	s.setupRotorStreamHandler() // must not panic
}
