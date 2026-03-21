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

func TestFilterTopicMatching(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	topic1 := types.HexToHash("0xaaaa")
	topic2 := types.HexToHash("0xbbbb")

	ch, err := svc.Subscribe(SubscriptionFilter{Topics: []types.Hash{topic1}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Dispatch matching topic
	svc.Dispatch(&Notification{
		Address:     types.HexToAddress("0x01"),
		Topic:       topic1,
		BlockNumber: 1,
		Timestamp:   time.Now(),
	})

	// Dispatch non-matching topic
	svc.Dispatch(&Notification{
		Address:     types.HexToAddress("0x01"),
		Topic:       topic2,
		BlockNumber: 2,
		Timestamp:   time.Now(),
	})

	// Should receive matching
	select {
	case got := <-ch.Receive():
		if got.BlockNumber != 1 {
			t.Fatalf("block = %d, want 1", got.BlockNumber)
		}
	case <-time.After(time.Second):
		t.Fatal("matching notification not received")
	}

	// Should NOT receive non-matching
	select {
	case <-ch.Receive():
		t.Fatal("should not receive non-matching topic notification")
	case <-time.After(50 * time.Millisecond):
		// OK
	}
}

func TestFilterCombinedAddressAndTopic(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	addr := types.HexToAddress("0x1111")
	topic := types.HexToHash("0xaaaa")

	ch, err := svc.Subscribe(SubscriptionFilter{
		Addresses: []types.Address{addr},
		Topics:    []types.Hash{topic},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Both match
	svc.Dispatch(&Notification{
		Address:     addr,
		Topic:       topic,
		BlockNumber: 1,
		Timestamp:   time.Now(),
	})
	// Address matches, topic doesn't
	svc.Dispatch(&Notification{
		Address:     addr,
		Topic:       types.HexToHash("0xbbbb"),
		BlockNumber: 2,
		Timestamp:   time.Now(),
	})
	// Topic matches, address doesn't
	svc.Dispatch(&Notification{
		Address:     types.HexToAddress("0x2222"),
		Topic:       topic,
		BlockNumber: 3,
		Timestamp:   time.Now(),
	})

	select {
	case got := <-ch.Receive():
		if got.BlockNumber != 1 {
			t.Fatalf("block = %d, want 1", got.BlockNumber)
		}
	case <-time.After(time.Second):
		t.Fatal("matching notification not received")
	}

	// No more notifications should arrive
	select {
	case n := <-ch.Receive():
		t.Fatalf("unexpected notification: block=%d", n.BlockNumber)
	case <-time.After(50 * time.Millisecond):
		// OK
	}
}

func TestFilterEmptyMatchesAll(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	ch, err := svc.Subscribe(SubscriptionFilter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	for i := 0; i < 3; i++ {
		svc.Dispatch(&Notification{
			Address:     types.HexToAddress("0x01"),
			Topic:       types.HexToHash("0xaa"),
			BlockNumber: uint64(i),
			Timestamp:   time.Now(),
		})
	}

	received := 0
	for i := 0; i < 3; i++ {
		select {
		case <-ch.Receive():
			received++
		case <-time.After(time.Second):
			t.Fatalf("timed out, received %d of 3", received)
		}
	}
	if received != 3 {
		t.Fatalf("received = %d, want 3", received)
	}
}

func TestHistoryCapacity(t *testing.T) {
	cfg := testNotifyConfig()
	cfg.MaxHistory = 3
	svc := NewService(cfg)
	svc.Start()
	defer svc.Stop()

	addr := types.HexToAddress("0x5555")
	// Dispatch 5 notifications, max history is 3
	for i := 0; i < 5; i++ {
		svc.Dispatch(&Notification{
			Address:     addr,
			BlockNumber: uint64(i),
			Timestamp:   time.Now(),
		})
	}

	hist := svc.History(addr, 0)
	if len(hist) != 3 {
		t.Fatalf("history len = %d, want 3", len(hist))
	}
	// Oldest should be block 2 (0,1 evicted)
	if hist[0].BlockNumber != 2 {
		t.Fatalf("oldest block = %d, want 2", hist[0].BlockNumber)
	}
	if hist[2].BlockNumber != 4 {
		t.Fatalf("newest block = %d, want 4", hist[2].BlockNumber)
	}
}

func TestHistoryMultipleAddresses(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	addr1 := types.HexToAddress("0xaa")
	addr2 := types.HexToAddress("0xbb")

	svc.Dispatch(&Notification{Address: addr1, BlockNumber: 1, Timestamp: time.Now()})
	svc.Dispatch(&Notification{Address: addr1, BlockNumber: 2, Timestamp: time.Now()})
	svc.Dispatch(&Notification{Address: addr2, BlockNumber: 10, Timestamp: time.Now()})

	hist1 := svc.History(addr1, 0)
	if len(hist1) != 2 {
		t.Fatalf("addr1 history len = %d, want 2", len(hist1))
	}

	hist2 := svc.History(addr2, 0)
	if len(hist2) != 1 {
		t.Fatalf("addr2 history len = %d, want 1", len(hist2))
	}
	if hist2[0].BlockNumber != 10 {
		t.Fatalf("addr2 block = %d, want 10", hist2[0].BlockNumber)
	}
}

func TestHistoryZeroLimit(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	addr := types.HexToAddress("0xcc")
	for i := 0; i < 4; i++ {
		svc.Dispatch(&Notification{Address: addr, BlockNumber: uint64(i), Timestamp: time.Now()})
	}

	hist := svc.History(addr, 0)
	if len(hist) != 4 {
		t.Fatalf("history len = %d, want 4 (all)", len(hist))
	}
}

func TestDispatchNoSubscribers(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	addr := types.HexToAddress("0xdd")

	// Dispatch with no subscribers - should not panic
	svc.Dispatch(&Notification{Address: addr, BlockNumber: 42, Timestamp: time.Now()})

	// History should still be recorded
	hist := svc.History(addr, 0)
	if len(hist) != 1 {
		t.Fatalf("history len = %d, want 1", len(hist))
	}
	if hist[0].BlockNumber != 42 {
		t.Fatalf("block = %d, want 42", hist[0].BlockNumber)
	}
}

func TestChannelBufferOverflow(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	// Create channel with buffer=1 directly
	ch := NewChannel("overflow-test", SubscriptionFilter{}, 1)

	// First send should succeed (fills buffer)
	ok := ch.Send(&Notification{BlockNumber: 1, Timestamp: time.Now()})
	if !ok {
		t.Fatal("first send should succeed")
	}

	// Second send should fail (buffer full, non-blocking)
	ok = ch.Send(&Notification{BlockNumber: 2, Timestamp: time.Now()})
	if ok {
		t.Fatal("second send should fail (buffer full)")
	}

	// Receive the first notification
	select {
	case got := <-ch.Receive():
		if got.BlockNumber != 1 {
			t.Fatalf("block = %d, want 1", got.BlockNumber)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification")
	}
}

func TestServiceStats(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	ch, _ := svc.Subscribe(SubscriptionFilter{})

	// Dispatch a notification
	svc.Dispatch(&Notification{
		Address:   types.HexToAddress("0x01"),
		Timestamp: time.Now(),
	})

	// Read to confirm delivery
	select {
	case <-ch.Receive():
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	stats := svc.Stats()
	if stats["subscribers"].(int) != 1 {
		t.Fatalf("subscribers = %v, want 1", stats["subscribers"])
	}
	if stats["sent"].(uint64) != 1 {
		t.Fatalf("sent = %v, want 1", stats["sent"])
	}
	if stats["dropped"].(uint64) != 0 {
		t.Fatalf("dropped = %v, want 0", stats["dropped"])
	}
}

func TestChannelCount(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	ch1, _ := svc.Subscribe(SubscriptionFilter{})
	svc.Subscribe(SubscriptionFilter{})
	svc.Subscribe(SubscriptionFilter{})

	if svc.ChannelCount() != 3 {
		t.Fatalf("ChannelCount = %d, want 3", svc.ChannelCount())
	}

	svc.Unsubscribe(ch1.id)

	if svc.ChannelCount() != 2 {
		t.Fatalf("ChannelCount after unsubscribe = %d, want 2", svc.ChannelCount())
	}
}

func TestHistoryCount(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	addr1 := types.HexToAddress("0xaa")
	addr2 := types.HexToAddress("0xbb")

	svc.Dispatch(&Notification{Address: addr1, Timestamp: time.Now()})
	svc.Dispatch(&Notification{Address: addr2, Timestamp: time.Now()})

	if svc.HistoryCount() != 2 {
		t.Fatalf("HistoryCount = %d, want 2", svc.HistoryCount())
	}
}

func TestClearHistory(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()
	defer svc.Stop()

	addr := types.HexToAddress("0xee")
	svc.Dispatch(&Notification{Address: addr, BlockNumber: 1, Timestamp: time.Now()})
	svc.Dispatch(&Notification{Address: addr, BlockNumber: 2, Timestamp: time.Now()})

	if len(svc.History(addr, 0)) != 2 {
		t.Fatal("expected 2 history entries before clear")
	}

	svc.ClearHistory()

	if len(svc.History(addr, 0)) != 0 {
		t.Fatal("expected 0 history entries after clear")
	}
	if svc.HistoryCount() != 0 {
		t.Fatalf("HistoryCount after clear = %d, want 0", svc.HistoryCount())
	}
}

func TestServiceStopClosesChannels(t *testing.T) {
	svc := NewService(testNotifyConfig())
	svc.Start()

	ch, _ := svc.Subscribe(SubscriptionFilter{})

	svc.Stop()

	// After Stop, the channel should be closed, so Receive() should be drained/closed
	select {
	case _, ok := <-ch.Receive():
		if ok {
			t.Fatal("expected channel to be closed after Stop")
		}
		// ok == false means closed, which is correct
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close")
	}
}

func TestChannelMatchesNilFilter(t *testing.T) {
	// Channel with empty filter should match any notification
	ch := NewChannel("nil-test", SubscriptionFilter{}, 10)

	n := &Notification{
		Address:   types.HexToAddress("0x01"),
		Topic:     types.HexToHash("0xaa"),
		Timestamp: time.Now(),
	}

	if !ch.Matches(n) {
		t.Fatal("empty filter should match all notifications")
	}
}
