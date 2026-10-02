// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"context"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
)

func protocolIDFor(topic string) protocol.ID { return protocol.ID(topic) }

// newLinkedServicePair returns two Services on a fully connected in-memory
// mocknet (no sockets). Callers must wait for the gossipsub mesh to form
// before publishing — see waitForTopicPeer.
func newLinkedServicePair(t *testing.T) (a, b *Service) {
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
	return newTestServiceOnHost(t, hosts[0]), newTestServiceOnHost(t, hosts[1])
}

// waitForTopicPeer polls until the topic handle sees at least one peer, or
// fails the test after a bounded wait. No sleeps over 100ms between checks.
func waitForTopicPeer(t *testing.T, topic *pubsub.Topic) {
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

func TestPublishAndSubscribeOverMocknet(t *testing.T) {
	a, b := newLinkedServicePair(t)

	const topic = "/n42/msg/shard/0"

	sub, err := b.SubscribeToTopic(topic)
	if err != nil {
		t.Fatalf("SubscribeToTopic: %v", err)
	}

	topicHandle, err := a.JoinTopic(topic)
	if err != nil {
		t.Fatalf("JoinTopic: %v", err)
	}
	waitForTopicPeer(t, topicHandle)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	payload := []byte("hello-mocknet")
	if err := a.PublishToTopic(ctx, topic, payload); err != nil {
		t.Fatalf("PublishToTopic: %v", err)
	}

	msg, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("sub.Next: %v", err)
	}
	if string(msg.Data) != string(payload) {
		t.Fatalf("received %q, want %q", msg.Data, payload)
	}
}

func TestJoinTopicIsIdempotentAndLeaveTopicRemoves(t *testing.T) {
	a, _ := newLinkedServicePair(t)
	const topic = "/n42/msg/shard/1"

	first, err := a.JoinTopic(topic)
	if err != nil {
		t.Fatalf("JoinTopic: %v", err)
	}
	second, err := a.JoinTopic(topic)
	if err != nil {
		t.Fatalf("JoinTopic (again): %v", err)
	}
	if first != second {
		t.Fatal("JoinTopic should return the same handle for an already-joined topic")
	}

	if err := a.LeaveTopic(topic); err != nil {
		t.Fatalf("LeaveTopic: %v", err)
	}
	if _, ok := a.joinedTopics[topic]; ok {
		t.Fatal("topic should be removed from joinedTopics after LeaveTopic")
	}
	// Leaving an already-left (unknown) topic is a no-op, not an error.
	if err := a.LeaveTopic(topic); err != nil {
		t.Fatalf("LeaveTopic on an unjoined topic: %v", err)
	}
}

func TestSendAndStreamHandlerOverMocknet(t *testing.T) {
	a, b := newLinkedServicePair(t)

	const baseTopic = "/n42/status/1.0.0"
	received := make(chan string, 1)
	b.SetStreamHandler(baseTopic+b.Encoding().ProtocolSuffix(), func(stream network.Stream) {
		defer stream.Close()
		buf := make([]byte, 64)
		n, _ := stream.Read(buf)
		received <- string(buf[:n])
	})

	stream, err := a.host.NewStream(context.Background(), b.host.ID(), protocolIDFor(baseTopic+a.Encoding().ProtocolSuffix()))
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("stream.Write: %v", err)
	}
	_ = stream.CloseWrite()

	select {
	case got := <-received:
		if got != "ping" {
			t.Fatalf("handler received %q, want %q", got, "ping")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the stream handler to run")
	}
}
