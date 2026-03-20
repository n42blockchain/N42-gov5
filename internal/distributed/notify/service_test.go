package notify

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
)

func testNotifyConfig() *conf.NotifyCfg {
	cfg := conf.DefaultNotifyCfg()
	cfg.Enabled = true
	cfg.MaxSubscribers = 10
	cfg.BufferSize = 16
	cfg.MaxHistory = 5
	return &cfg
}

func TestSubscribeAndDispatch(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	addr := types.HexToAddress("0x1111")
	ch, err := svc.Subscribe(SubscriptionFilter{Addresses: []types.Address{addr}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	n := &Notification{
		ID:          types.HexToHash("0xaa"),
		Address:     addr,
		BlockNumber: 100,
		Timestamp:   time.Now(),
	}
	svc.Dispatch(n)

	select {
	case got := <-ch.Receive():
		if got.BlockNumber != 100 {
			t.Fatalf("block = %d, want 100", got.BlockNumber)
		}
	case <-time.After(time.Second):
		t.Fatal("notification not received")
	}
}

func TestFilterNonMatching(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	addr1 := types.HexToAddress("0x1111")
	addr2 := types.HexToAddress("0x2222")
	ch, _ := svc.Subscribe(SubscriptionFilter{Addresses: []types.Address{addr1}})

	svc.Dispatch(&Notification{Address: addr2, Timestamp: time.Now()})

	select {
	case <-ch.Receive():
		t.Fatal("should not receive non-matching notification")
	case <-time.After(50 * time.Millisecond):
		// OK
	}
}

func TestMaxSubscribers(t *testing.T) {
	cfg := testNotifyConfig()
	cfg.MaxSubscribers = 2
	svc := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	svc.Subscribe(SubscriptionFilter{})
	svc.Subscribe(SubscriptionFilter{})
	_, err := svc.Subscribe(SubscriptionFilter{})
	if err != ErrMaxSubscribers {
		t.Fatalf("expected ErrMaxSubscribers, got %v", err)
	}
}

func TestHistory(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	addr := types.HexToAddress("0x3333")
	for i := 0; i < 10; i++ {
		svc.Dispatch(&Notification{
			Address:     addr,
			BlockNumber: uint64(i),
			Timestamp:   time.Now(),
		})
	}

	hist := svc.History(addr, 3)
	if len(hist) != 3 {
		t.Fatalf("history len = %d, want 3", len(hist))
	}
	// Should be most recent
	if hist[0].BlockNumber != 7 {
		t.Fatalf("oldest in history = %d, want 7", hist[0].BlockNumber)
	}
}

func TestUnsubscribe(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	ch, _ := svc.Subscribe(SubscriptionFilter{})
	svc.Unsubscribe(ch.id)

	stats := svc.Stats()
	if stats["subscribers"].(int) != 0 {
		t.Fatal("subscriber should be removed")
	}
}

func TestChannelClose(t *testing.T) {
	ch := NewChannel("test", SubscriptionFilter{}, 10)
	ch.Send(&Notification{Timestamp: time.Now()})
	ch.Close()
	ch.Close() // double close safe

	if ch.Send(&Notification{Timestamp: time.Now()}) {
		t.Fatal("send to closed channel should return false")
	}
}

func TestServiceLifecycle(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	svc.Stop()
	svc.Stop() // double stop safe
}
