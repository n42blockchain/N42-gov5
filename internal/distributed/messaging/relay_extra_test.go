package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/n42blockchain/N42/crypto"
)

// fakePublisher is an in-process P2PPublisher fake: PublishToNetwork never
// needs SubscribeToTopic, so that method only has to satisfy the interface.
type fakePublisher struct {
	mu        sync.Mutex
	published []publishedMsg
	err       error
}

type publishedMsg struct {
	topic string
	data  []byte
}

func (f *fakePublisher) PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, publishedMsg{topic: topic, data: append([]byte(nil), data...)})
	return nil
}

func (f *fakePublisher) SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	return nil, errors.New("not implemented in fakePublisher")
}

func (f *fakePublisher) snapshot() []publishedMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]publishedMsg, len(f.published))
	copy(out, f.published)
	return out
}

// TestRelay_PublishToNetwork_Unsigned covers PublishToNetwork's happy path
// without a signing key: the envelope is encoded and handed to the
// P2PPublisher on the shard topic derived from topicShard, and the
// envelope's id is marked seen so a later echo of the same message is
// dropped by handleIncoming's dedup check.
func TestRelay_PublishToNetwork_Unsigned(t *testing.T) {
	cfg := testRelayConfig()
	svc := NewService(cfg)
	pub := &fakePublisher{}
	relay := NewRelay(cfg, svc, pub, nil)

	msg := &Message{
		Topic:     "my/topic",
		Payload:   []byte("hello network"),
		Timestamp: time.Now(),
	}

	if err := relay.PublishToNetwork(msg); err != nil {
		t.Fatalf("PublishToNetwork() error = %v", err)
	}

	sent := pub.snapshot()
	if len(sent) != 1 {
		t.Fatalf("published %d messages, want 1", len(sent))
	}

	env, err := DecodeEnvelope(sent[0].data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	if env.Topic != msg.Topic || string(env.Payload) != string(msg.Payload) {
		t.Fatalf("decoded envelope = %+v, want topic/payload matching %+v", env, msg)
	}

	wantShard := relay.topicShard(msg.Topic)
	wantTopic := fmt.Sprintf(MessageTopicFormat, wantShard)
	if sent[0].topic != wantTopic {
		t.Fatalf("published to topic %q, want %q", sent[0].topic, wantTopic)
	}

	if !relay.isSeen(EnvelopeID(env)) {
		t.Fatal("expected PublishToNetwork to mark its own envelope id as seen")
	}
}

// TestRelay_PublishToNetwork_Signed covers the signing branch: a non-nil key
// must produce a verifiable signature on the outgoing envelope.
func TestRelay_PublishToNetwork_Signed(t *testing.T) {
	cfg := testRelayConfig()
	svc := NewService(cfg)
	pub := &fakePublisher{}

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	relay := NewRelay(cfg, svc, pub, key)

	msg := &Message{Topic: "signed/topic", Payload: []byte("signed"), Timestamp: time.Now()}
	if err := relay.PublishToNetwork(msg); err != nil {
		t.Fatalf("PublishToNetwork() error = %v", err)
	}

	sent := pub.snapshot()
	if len(sent) != 1 {
		t.Fatalf("published %d messages, want 1", len(sent))
	}
	env, err := DecodeEnvelope(sent[0].data)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	if len(env.Signature) == 0 {
		t.Fatal("expected a non-empty signature on a signed envelope")
	}
	if err := ValidateEnvelope(env, MaxEnvelopeSize); err != nil {
		t.Fatalf("ValidateEnvelope() error = %v, want a valid signed envelope", err)
	}
}

// TestRelay_PublishToNetwork_PublishError covers propagation of a
// P2PPublisher failure.
func TestRelay_PublishToNetwork_PublishError(t *testing.T) {
	cfg := testRelayConfig()
	svc := NewService(cfg)
	pub := &fakePublisher{err: errors.New("network down")}
	relay := NewRelay(cfg, svc, pub, nil)

	msg := &Message{Topic: "t", Payload: []byte("x"), Timestamp: time.Now()}
	if err := relay.PublishToNetwork(msg); err == nil {
		t.Fatal("PublishToNetwork() error = nil, want propagated publish error")
	}
}

// TestRelay_HandleIncoming_DedupsOwnPublish exercises handleIncoming
// end-to-end with a fake publisher's recorded bytes, verifying a message
// this relay just published is dropped as already-seen rather than being
// redelivered to the local service.
func TestRelay_HandleIncoming_DedupsOwnPublish(t *testing.T) {
	cfg := testRelayConfig()
	svc := NewService(cfg)
	pub := &fakePublisher{}
	relay := NewRelay(cfg, svc, pub, nil)

	msg := &Message{Topic: "dedup/topic", Payload: []byte("once"), Timestamp: time.Now()}
	if err := relay.PublishToNetwork(msg); err != nil {
		t.Fatalf("PublishToNetwork() error = %v", err)
	}

	sent := pub.snapshot()
	before := len(svc.GetMessages("dedup/topic", time.Time{}, time.Time{}, 10))

	// Feed the exact bytes we just "sent" back in, simulating our own
	// GossipSub echo.
	relay.handleIncoming(sent[0].data)

	after := len(svc.GetMessages("dedup/topic", time.Time{}, time.Time{}, 10))
	if after != before {
		t.Fatalf("expected no new stored message from a deduped echo: before=%d after=%d", before, after)
	}
}

