package messaging

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// TestStore_Get covers the Store.Get hit and miss branches.
func TestStore_Get(t *testing.T) {
	s := NewStore(10, time.Hour)
	msg := &Message{ID: types.Hash{0x01}, Topic: "t"}
	s.Add(msg)

	got, ok := s.Get(types.Hash{0x01})
	if !ok || got != msg {
		t.Fatalf("Get() = (%v, %v), want (%v, true)", got, ok, msg)
	}

	_, ok = s.Get(types.Hash{0xFF})
	if ok {
		t.Fatal("Get() found a message that was never added")
	}
}

// TestService_Unsubscribe covers removing a handler by pointer equality,
// including the branch that deletes the topic entry once its handler list
// becomes empty, and the no-op case of unsubscribing an unknown handler.
func TestService_Unsubscribe(t *testing.T) {
	svc := NewService(testMsgConfig())

	var calls int
	h1 := func(m *Message) { calls++ }
	h2 := func(m *Message) { calls += 10 }

	svc.Subscribe("topic", h1)
	svc.Subscribe("topic", h2)

	svc.Unsubscribe("topic", h1)
	svc.mu.RLock()
	remaining := len(svc.handlers["topic"])
	svc.mu.RUnlock()
	if remaining != 1 {
		t.Fatalf("handlers remaining = %d, want 1", remaining)
	}

	svc.Unsubscribe("topic", h2)
	svc.mu.RLock()
	_, exists := svc.handlers["topic"]
	svc.mu.RUnlock()
	if exists {
		t.Fatal("expected topic entry to be deleted once its handler list is empty")
	}

	// Unsubscribing a handler that was never registered must be a no-op.
	svc.Unsubscribe("topic", h1)
	svc.Unsubscribe("never-subscribed", h1)
}

// TestService_Stats covers the Stats snapshot.
func TestService_Stats(t *testing.T) {
	svc := NewService(testMsgConfig())
	svc.Start()
	defer svc.Stop()

	if _, err := svc.Publish("t", []byte("x"), "", "peer1"); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	stats := svc.Stats()
	if stats["published"].(uint64) != 1 {
		t.Fatalf("stats[published] = %v, want 1", stats["published"])
	}
	if stats["storeSize"].(int) != 1 {
		t.Fatalf("stats[storeSize] = %v, want 1", stats["storeSize"])
	}
}

// TestService_SetRelay covers wiring a relay onto the service.
func TestService_SetRelay(t *testing.T) {
	svc := NewService(testMsgConfig())
	relay := NewRelay(testMsgConfig(), svc, nil, nil)
	svc.SetRelay(relay)
	if svc.relay != relay {
		t.Fatal("SetRelay() did not store the relay")
	}
}

// TestRateLimiter_PruneStale covers both branches: a sender whose entire
// window has expired is removed, while a sender with a recent entry is kept.
func TestRateLimiter_PruneStale(t *testing.T) {
	rl := NewRateLimiter(100)
	rl.windowLen = 10 * time.Millisecond

	rl.Allow("stale-sender")
	time.Sleep(20 * time.Millisecond)
	rl.Allow("fresh-sender")

	rl.PruneStale()

	rl.mu.Lock()
	_, staleExists := rl.windows["stale-sender"]
	_, freshExists := rl.windows["fresh-sender"]
	rl.mu.Unlock()

	if staleExists {
		t.Fatal("expected stale-sender's window to be pruned")
	}
	if !freshExists {
		t.Fatal("expected fresh-sender's window to be kept")
	}
}
