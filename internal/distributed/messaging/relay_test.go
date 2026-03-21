package messaging

import (
	"fmt"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
)

func testRelayConfig() *conf.MessagingCfg {
	cfg := conf.DefaultMessagingCfg()
	cfg.Enabled = true
	cfg.StoreCapacity = 100
	cfg.StoreTTLSec = 60
	cfg.RLNRateLimit = 100
	cfg.MessageShards = 4
	cfg.MaxEnvelopeSize = MaxEnvelopeSize
	cfg.DeduplicateCacheSize = 64
	return &cfg
}

// --- Tests for internal helper methods (markSeen, isSeen, topicShard) ---

func TestRelaySeenCache(t *testing.T) {
	cfg := testRelayConfig()
	cfg.DeduplicateCacheSize = 10
	svc := NewService(cfg)
	relay := NewRelay(cfg, svc, nil, nil)

	id1 := types.Hash{1}
	id2 := types.Hash{2}
	id3 := types.Hash{3}

	// Initially nothing is seen.
	if relay.isSeen(id1) {
		t.Fatal("id1 should not be seen initially")
	}

	// Mark and check.
	relay.markSeen(id1)
	if !relay.isSeen(id1) {
		t.Fatal("id1 should be seen after markSeen")
	}

	// Different IDs are independent.
	if relay.isSeen(id2) {
		t.Fatal("id2 should not be seen")
	}

	relay.markSeen(id2)
	relay.markSeen(id3)

	if !relay.isSeen(id1) {
		t.Fatal("id1 should still be seen")
	}
	if !relay.isSeen(id2) {
		t.Fatal("id2 should be seen")
	}
	if !relay.isSeen(id3) {
		t.Fatal("id3 should be seen")
	}

	// Marking the same ID twice is a no-op (no crash, still seen).
	relay.markSeen(id1)
	if !relay.isSeen(id1) {
		t.Fatal("id1 should still be seen after double mark")
	}
}

func TestRelayDeduplication(t *testing.T) {
	cfg := testRelayConfig()
	svc := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	relay := NewRelay(cfg, svc, nil, key)

	// Subscribe to collect delivered messages.
	received := make(chan *Message, 10)
	svc.Subscribe("dedup-topic", func(msg *Message) {
		received <- msg
	})

	// Create a signed envelope.
	env := &Envelope{
		Version:   EnvelopeVersion1,
		Topic:     "dedup-topic",
		Payload:   []byte("dedup-test"),
		Timestamp: time.Now().UnixNano(),
		Nonce:     1,
	}
	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}

	data, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	// First delivery should succeed.
	relay.handleIncoming(data)

	select {
	case <-received:
		// ok
	case <-time.After(time.Second):
		t.Fatal("first message should be delivered")
	}

	// Second delivery of the same message should be deduped (silently dropped).
	relay.handleIncoming(data)

	select {
	case <-received:
		t.Fatal("duplicate message should NOT be delivered")
	case <-time.After(50 * time.Millisecond):
		// ok - no message delivered, dedup worked
	}
}

func TestRelaySeenCacheEviction(t *testing.T) {
	cfg := testRelayConfig()
	cfg.DeduplicateCacheSize = 5
	svc := NewService(cfg)
	relay := NewRelay(cfg, svc, nil, nil)

	// Fill cache to capacity.
	ids := make([]types.Hash, 10)
	for i := 0; i < 10; i++ {
		ids[i] = types.Hash{byte(i)}
		relay.markSeen(ids[i])
	}

	// The cache size is capped at 5. The oldest entries should have been evicted.
	// IDs 0-4 should be evicted, IDs 5-9 should be present.
	for i := 0; i < 5; i++ {
		if relay.isSeen(ids[i]) {
			t.Errorf("id %d should have been evicted", i)
		}
	}
	for i := 5; i < 10; i++ {
		if !relay.isSeen(ids[i]) {
			t.Errorf("id %d should still be in cache", i)
		}
	}

	// Verify the cache map size is exactly at max.
	relay.seenMu.Lock()
	cacheSize := len(relay.seenCache)
	relay.seenMu.Unlock()
	if cacheSize != 5 {
		t.Errorf("cache size = %d, want 5", cacheSize)
	}
}

func TestRelayTopicSharding(t *testing.T) {
	cfg := testRelayConfig()
	cfg.MessageShards = 8
	svc := NewService(cfg)
	relay := NewRelay(cfg, svc, nil, nil)

	// Same topic should always get the same shard (deterministic).
	topic := "chat/general"
	shard1 := relay.topicShard(topic)
	shard2 := relay.topicShard(topic)
	if shard1 != shard2 {
		t.Fatalf("topicShard not deterministic: %d != %d", shard1, shard2)
	}

	// Result must be in [0, MessageShards).
	if shard1 < 0 || shard1 >= 8 {
		t.Fatalf("shard %d out of range [0, 8)", shard1)
	}

	// Different topics should (generally) produce different shards.
	// We test with enough topics that at least 2 different shards appear.
	shardsSeen := make(map[int]bool)
	for i := 0; i < 20; i++ {
		s := relay.topicShard(fmt.Sprintf("topic-%d", i))
		if s < 0 || s >= 8 {
			t.Fatalf("shard %d out of range for topic-%d", s, i)
		}
		shardsSeen[s] = true
	}
	if len(shardsSeen) < 2 {
		t.Fatal("expected at least 2 different shards across 20 topics")
	}
}

func TestRelayHandleIncomingValid(t *testing.T) {
	cfg := testRelayConfig()
	svc := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	relay := NewRelay(cfg, svc, nil, key)

	received := make(chan *Message, 1)
	svc.Subscribe("incoming-topic", func(msg *Message) {
		received <- msg
	})

	env := &Envelope{
		Version:   EnvelopeVersion1,
		Topic:     "incoming-topic",
		Payload:   []byte("hello from relay"),
		Timestamp: time.Now().UnixNano(),
		Nonce:     42,
	}
	if err := SignEnvelope(env, key); err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}
	data, err := EncodeEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	relay.handleIncoming(data)

	select {
	case msg := <-received:
		if msg.Topic != "incoming-topic" {
			t.Errorf("Topic = %q, want %q", msg.Topic, "incoming-topic")
		}
		if string(msg.Payload) != "hello from relay" {
			t.Errorf("Payload = %q, want %q", msg.Payload, "hello from relay")
		}
	case <-time.After(time.Second):
		t.Fatal("message was not delivered to service handler")
	}
}

func TestRelayHandleIncomingInvalid(t *testing.T) {
	cfg := testRelayConfig()
	svc := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	relay := NewRelay(cfg, svc, nil, nil)

	received := make(chan *Message, 1)
	svc.Subscribe("bad-topic", func(msg *Message) {
		received <- msg
	})

	// Garbage data should be silently dropped.
	relay.handleIncoming([]byte{0xFF, 0x00})

	select {
	case <-received:
		t.Fatal("garbage data should not be delivered")
	case <-time.After(50 * time.Millisecond):
		// ok
	}
}

func TestRelayStartStop(t *testing.T) {
	cfg := testRelayConfig()
	svc := NewService(cfg)
	// Create relay with nil P2P (Start will fail to subscribe but should not panic).
	// We only test that the lifecycle does not hang or crash.
	relay := NewRelay(cfg, svc, nil, nil)

	// Stop without Start should not panic or deadlock.
	relay.Stop()

	// Verify the context is cancelled.
	select {
	case <-relay.ctx.Done():
		// ok
	default:
		t.Fatal("relay context should be cancelled after Stop")
	}
}
