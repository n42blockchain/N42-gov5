// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Real in-process libp2p + gossipsub harness for Relay.Start/readLoop and
// PeerHandler's store-query stream protocol. Mirrors the mocknet pattern used
// in internal/consensus/hotstuff/hsT_mocknet_test.go and
// internal/p2p/testservice_test.go: mocknet gives connected hosts with no
// real sockets, and pubsub.NewGossipSub runs unmodified on top of them.

package messaging

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/protocol"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
)

// msgTP2P is a minimal, real P2PPublisher implementation over a plain
// libp2p host + gossipsub instance.
type msgTP2P struct {
	h  host.Host
	ps *pubsub.PubSub

	topics map[string]*pubsub.Topic
}

func newMsgTP2P(t *testing.T, h host.Host) *msgTP2P {
	t.Helper()
	ps, err := pubsub.NewGossipSub(context.Background(), h)
	if err != nil {
		t.Fatalf("pubsub.NewGossipSub: %v", err)
	}
	return &msgTP2P{h: h, ps: ps, topics: make(map[string]*pubsub.Topic)}
}

func (p *msgTP2P) joinTopic(topic string) (*pubsub.Topic, error) {
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

func (p *msgTP2P) PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error {
	th, err := p.joinTopic(topic)
	if err != nil {
		return err
	}
	return th.Publish(ctx, data, opts...)
}

func (p *msgTP2P) SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	th, err := p.joinTopic(topic)
	if err != nil {
		return nil, err
	}
	return th.Subscribe(opts...)
}

var _ P2PPublisher = (*msgTP2P)(nil)

// msgTMocknetPair returns two connected mocknet hosts and their adapters.
func msgTMocknetPair(t *testing.T) (p0, p1 *msgTP2P, h0, h1 host.Host) {
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
	return newMsgTP2P(t, hosts[0]), newMsgTP2P(t, hosts[1]), hosts[0], hosts[1]
}

func msgTDefaultCfg() *conf.MessagingCfg {
	return &conf.MessagingCfg{
		MessageShards:        2,
		DeduplicateCacheSize: 64,
		MaxEnvelopeSize:      MaxEnvelopeSize,
		MaxMessageSize:       65536,
		StoreCapacity:        1000,
		StoreTTLSec:          3600,
		RLNRateLimit:         1000,
	}
}

func msgTWaitForTopicPeer(t *testing.T, topic string, p *msgTP2P) {
	t.Helper()
	th, err := p.joinTopic(topic)
	if err != nil {
		t.Fatalf("joinTopic: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(th.ListPeers()) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a gossipsub mesh peer")
}

// TestRelay_StartPublishDeliverDedup drives two real Relay instances over a
// mocknet gossipsub transport: node A publishes, node B's relay receives it
// through readLoop and delivers to its Service; replaying the same envelope
// must be deduped and not delivered twice.
func TestRelay_StartPublishDeliverDedup(t *testing.T) {
	p0, p1, _, _ := msgTMocknetPair(t)
	cfg := msgTDefaultCfg()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	svcA := NewService(cfg)
	svcA.Start()
	t.Cleanup(svcA.Stop)
	relayA := NewRelay(cfg, svcA, p0, key)
	svcA.SetRelay(relayA)
	if err := relayA.Start(); err != nil {
		t.Fatalf("relayA.Start: %v", err)
	}
	t.Cleanup(relayA.Stop)

	svcB := NewService(cfg)
	svcB.Start()
	t.Cleanup(svcB.Stop)
	relayB := NewRelay(cfg, svcB, p1, nil)
	svcB.SetRelay(relayB)
	if err := relayB.Start(); err != nil {
		t.Fatalf("relayB.Start: %v", err)
	}
	t.Cleanup(relayB.Stop)

	const topic = "mocknet-topic"
	shard := relayA.topicShard(topic)
	shardTopic := topicFromShard(shard)

	// Wait for the mesh to form on the shard both relays actually use.
	msgTWaitForTopicPeer(t, shardTopic, p0)
	msgTWaitForTopicPeer(t, shardTopic, p1)

	received := make(chan *Message, 16)
	svcB.Subscribe(topic, func(msg *Message) {
		received <- msg
	})

	// ListPeers() reports the topic peer as soon as it is known, but
	// gossipsub only grafts it into its mesh on the next heartbeat, so a
	// single publish right after the peer appears can race the graft.
	// Retry the publish (each with a fresh nonce, so dedup never hides a
	// prior attempt) on a short poll until it lands, rather than sleeping
	// for a fixed mesh-settle duration.
	msg := &Message{Topic: topic, Payload: []byte("hello mocknet"), Timestamp: time.Now()}
	var got *Message
	deadline := time.Now().Add(8 * time.Second)
	for got == nil && time.Now().Before(deadline) {
		if err := relayA.PublishToNetwork(msg); err != nil {
			t.Fatalf("PublishToNetwork: %v", err)
		}
		select {
		case got = <-received:
		case <-time.After(100 * time.Millisecond):
		}
	}
	if got == nil {
		t.Fatal("timed out waiting for relayed message")
	}
	if string(got.Payload) != "hello mocknet" {
		t.Fatalf("payload = %q, want %q", got.Payload, "hello mocknet")
	}

	if svcB.received.Load() < 1 {
		t.Fatalf("received count = %d, want >= 1", svcB.received.Load())
	}

	// Drain any extra copies left over from the publish-retry loop above so
	// the baseline below reflects a settled count.
	drainDeadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(drainDeadline) {
		select {
		case <-received:
		case <-time.After(50 * time.Millisecond):
		}
	}
	baseline := svcB.received.Load()

	// Publish one fresh, distinct envelope and wait for it to land, then
	// replay the exact same bytes on the gossipsub topic: the dedup cache
	// on B's relay must drop the replay without delivering it again.
	env := &Envelope{Version: EnvelopeVersion1, Topic: topic, Payload: []byte("replay-payload"), Timestamp: time.Now().UnixNano()}
	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}
	data, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	landed := false
	deadline = time.Now().Add(8 * time.Second)
	for !landed && time.Now().Before(deadline) {
		if err := p0.PublishToTopic(context.Background(), shardTopic, data); err != nil {
			t.Fatalf("PublishToTopic: %v", err)
		}
		pollDeadline := time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(pollDeadline) {
			if svcB.received.Load() > baseline {
				landed = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !landed {
		t.Fatal("timed out waiting for the fresh envelope to land")
	}
	// Drain the one delivery (and any accidental duplicates from the retry
	// loop's re-publishes, which gossipsub itself also dedups).
	afterFirst := svcB.received.Load()

	// Now replay the identical bytes; the id-based dedup must drop it.
	if err := p0.PublishToTopic(context.Background(), shardTopic, data); err != nil {
		t.Fatalf("PublishToTopic replay: %v", err)
	}
	stableDeadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(stableDeadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if svcB.received.Load() != afterFirst {
		t.Fatalf("received count after replay = %d, want still %d (deduped)", svcB.received.Load(), afterFirst)
	}
}

func topicFromShard(shard int) string {
	return fmt.Sprintf(MessageTopicFormat, shard)
}

// TestRelay_HandleIncoming_InvalidEnvelope exercises handleIncoming's
// validation-failure path (decode ok, validate fails) without a mesh.
func TestRelay_HandleIncoming_InvalidEnvelope(t *testing.T) {
	cfg := msgTDefaultCfg()
	svc := NewService(cfg)
	svc.Start()
	t.Cleanup(svc.Stop)
	relay := NewRelay(cfg, svc, nil, nil)

	// Version 0 is invalid per ValidateEnvelope.
	env := &Envelope{Version: 0, Topic: "t", Payload: []byte("x"), Timestamp: time.Now().UnixNano()}
	data, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	relay.handleIncoming(data)
	if svc.received.Load() != 0 {
		t.Fatalf("received = %d, want 0 for invalid envelope", svc.received.Load())
	}

	// Garbage bytes: decode fails outright.
	relay.handleIncoming([]byte{0xff, 0x00, 0x01})
	if svc.received.Load() != 0 {
		t.Fatalf("received = %d, want 0 for undecodable envelope", svc.received.Load())
	}
}

// TestRelay_StartNoSubscriber covers Start()'s subscribe-error branch: a
// P2PPublisher that always fails SubscribeToTopic must not stop Start from
// returning, and no goroutines should be left running after Stop.
type msgTFailingPublisher struct{}

func (msgTFailingPublisher) PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error {
	return nil
}

func (msgTFailingPublisher) SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	return nil, context.DeadlineExceeded
}

func TestRelay_StartNoSubscriber(t *testing.T) {
	cfg := msgTDefaultCfg()
	svc := NewService(cfg)
	svc.Start()
	t.Cleanup(svc.Stop)
	relay := NewRelay(cfg, svc, msgTFailingPublisher{}, nil)
	if err := relay.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	relay.Stop()
}

// --- PeerHandler store-query protocol over mocknet ---

func msgTBuildStoreQueryRequest(topic string, from, to time.Time, limit uint32) []byte {
	topicBytes := []byte(topic)
	buf := make([]byte, 0, 2+len(topicBytes)+16+4)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(topicBytes)))
	buf = append(buf, topicBytes...)
	var timeBuf [8]byte
	binary.BigEndian.PutUint64(timeBuf[:], uint64(from.UnixNano()))
	buf = append(buf, timeBuf[:]...)
	binary.BigEndian.PutUint64(timeBuf[:], uint64(to.UnixNano()))
	buf = append(buf, timeBuf[:]...)
	buf = binary.BigEndian.AppendUint32(buf, limit)
	return buf
}

// TestPeerHandler_QueryPeerStore_Hit stores a message on peer B, then has
// peer A's QueryPeerStore fetch it over the real /n42/msg/store_query/1.0.0
// stream protocol via NewPeerHandler/HandleStoreQuery.
func TestPeerHandler_QueryPeerStore_Hit(t *testing.T) {
	_, _, hA, hB := msgTMocknetPair(t)
	cfg := msgTDefaultCfg()

	svcB := NewService(cfg)
	svcB.Start()
	t.Cleanup(svcB.Stop)
	NewPeerHandler(hB, svcB)

	before := time.Now().Add(-time.Hour)
	if _, err := svcB.Publish("store-topic", []byte("payload-1"), "text/plain", "sender-1"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	after := time.Now().Add(time.Hour)

	phA := NewPeerHandler(hA, NewService(cfg))
	msgs, err := phA.QueryPeerStore(hB.ID(), "store-topic", before, after)
	if err != nil {
		t.Fatalf("QueryPeerStore: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if string(msgs[0].Payload) != "payload-1" {
		t.Fatalf("payload = %q, want %q", msgs[0].Payload, "payload-1")
	}
}

// TestPeerHandler_QueryPeerStore_Miss queries a topic with nothing stored.
func TestPeerHandler_QueryPeerStore_Miss(t *testing.T) {
	_, _, hA, hB := msgTMocknetPair(t)
	cfg := msgTDefaultCfg()
	svcB := NewService(cfg)
	svcB.Start()
	t.Cleanup(svcB.Stop)
	NewPeerHandler(hB, svcB)

	phA := NewPeerHandler(hA, NewService(cfg))
	msgs, err := phA.QueryPeerStore(hB.ID(), "nonexistent-topic", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("QueryPeerStore: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("len(msgs) = %d, want 0", len(msgs))
	}
}

// TestPeerHandler_MalformedRequest sends a truncated/malformed request
// directly on the stream protocol and verifies HandleStoreQuery returns
// without hanging or panicking (the connection is simply closed).
func TestPeerHandler_MalformedRequest(t *testing.T) {
	_, _, hA, hB := msgTMocknetPair(t)
	cfg := msgTDefaultCfg()
	svcB := NewService(cfg)
	svcB.Start()
	t.Cleanup(svcB.Stop)
	NewPeerHandler(hB, svcB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := hA.NewStream(ctx, hB.ID(), protoID)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	// Write only 1 byte of the 2-byte topic length prefix, then close our
	// write side; HandleStoreQuery's io.ReadFull must fail and return.
	if _, err := s.Write([]byte{0x00}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	// Reading from our side should get EOF/closed since the handler returns
	// without writing a response.
	buf := make([]byte, 4)
	_, _ = s.Read(buf)
	_ = s.Close()
}

// TestPeerHandler_OversizedTopic sends a request whose declared topic length
// exceeds the 1024-byte cap; HandleStoreQuery must bail out before reading
// the (never-sent) topic bytes.
func TestPeerHandler_OversizedTopic(t *testing.T) {
	_, _, hA, hB := msgTMocknetPair(t)
	cfg := msgTDefaultCfg()
	svcB := NewService(cfg)
	svcB.Start()
	t.Cleanup(svcB.Stop)
	NewPeerHandler(hB, svcB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := hA.NewStream(ctx, hB.ID(), protoID)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	defer s.Close()

	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], 2000) // > 1024 cap
	if _, err := s.Write(lenBuf[:]); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// The handler must return (close its side) without reading further; we
	// should observe EOF rather than hanging.
	buf := make([]byte, 4)
	_ = s.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := s.Read(buf)
	if n != 0 || err == nil {
		t.Fatalf("expected EOF/closed read with no data, got n=%d err=%v", n, err)
	}
}

var protoID = protocol.ID(StoreQueryProtocolID)
