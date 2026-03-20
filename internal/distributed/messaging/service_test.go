package messaging

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
)

func testMsgConfig() *conf.MessagingCfg {
	cfg := conf.DefaultMessagingCfg()
	cfg.Enabled = true
	cfg.StoreCapacity = 100
	cfg.StoreTTLSec = 2
	cfg.RLNRateLimit = 5
	return &cfg
}

func TestPublishAndRetrieve(t *testing.T) {
	svc := NewService(testMsgConfig())
	svc.Start()
	defer svc.Stop()

	id, err := svc.Publish("test/topic", []byte("hello"), "text/plain", "peer1")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if id == ([32]byte{}) {
		t.Fatal("expected non-zero message ID")
	}

	msgs := svc.GetMessages("test/topic", time.Time{}, time.Time{}, 10)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if string(msgs[0].Payload) != "hello" {
		t.Fatalf("payload = %s, want hello", msgs[0].Payload)
	}
}

func TestRateLimit(t *testing.T) {
	cfg := testMsgConfig()
	cfg.RLNRateLimit = 2
	svc := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	svc.Publish("t", []byte("1"), "", "peer1")
	svc.Publish("t", []byte("2"), "", "peer1")
	_, err := svc.Publish("t", []byte("3"), "", "peer1")
	if err == nil {
		t.Fatal("expected rate limit error")
	}
}

func TestMessageTooLarge(t *testing.T) {
	cfg := testMsgConfig()
	cfg.MaxMessageSize = 10
	svc := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	_, err := svc.Publish("t", make([]byte, 20), "", "p")
	if err == nil {
		t.Fatal("expected message too large error")
	}
}

func TestStoreCapacity(t *testing.T) {
	cfg := testMsgConfig()
	cfg.StoreCapacity = 3
	store := NewStore(cfg.StoreCapacity, time.Hour)

	for i := 0; i < 5; i++ {
		store.Add(&Message{
			ID:        [32]byte{byte(i)},
			Topic:     "t",
			Payload:   []byte{byte(i)},
			Timestamp: time.Now(),
		})
	}
	if store.Size() > 3 {
		t.Fatalf("store size = %d, should be <= 3", store.Size())
	}
}

func TestStorePrune(t *testing.T) {
	store := NewStore(100, 1*time.Millisecond)
	store.Add(&Message{ID: [32]byte{1}, Topic: "t", Timestamp: time.Now()})
	time.Sleep(5 * time.Millisecond)
	pruned := store.Prune()
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
}

func TestSubscribeHandler(t *testing.T) {
	svc := NewService(testMsgConfig())
	svc.Start()
	defer svc.Stop()

	received := make(chan *Message, 1)
	svc.Subscribe("events", func(msg *Message) {
		received <- msg
	})

	svc.Publish("events", []byte("event-data"), "app/json", "p1")

	select {
	case msg := <-received:
		if string(msg.Payload) != "event-data" {
			t.Fatalf("wrong payload: %s", msg.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("handler not called")
	}
}

func TestServiceLifecycle(t *testing.T) {
	svc := NewService(testMsgConfig())
	svc.Start()
	svc.Stop()
	svc.Stop() // double stop safe
}
