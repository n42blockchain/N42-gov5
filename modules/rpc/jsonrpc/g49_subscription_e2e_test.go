package jsonrpc

import (
	"context"
	"errors"
	"testing"
)

// g49FakeConn is a minimal jsonWriter for driving Notifier.send/activate
// directly, without a real codec/pipe.
type g49FakeConn struct {
	written  []interface{}
	closedCh chan interface{}
}

func newG49FakeConn() *g49FakeConn {
	return &g49FakeConn{closedCh: make(chan interface{})}
}

func (c *g49FakeConn) writeJSON(ctx context.Context, msg interface{}) error {
	c.written = append(c.written, msg)
	return nil
}
func (c *g49FakeConn) closed() <-chan interface{} { return c.closedCh }
func (c *g49FakeConn) remoteAddr() string         { return "g49-fake" }

func g49NewNotifier() (*Notifier, *g49FakeConn) {
	conn := newG49FakeConn()
	h := &handler{idgen: func() ID { return ID("0x1") }, conn: conn}
	return &Notifier{h: h, namespace: "g49"}, conn
}

// TestNotifier_CreateSubscription_RejectsSecondCall covers
// ErrMultipleSubscriptions (CreateSubscription called twice on one
// Notifier) and ErrSubscriptionAfterReturn (called after takeSubscription
// has already marked the callback as returned).
func TestNotifier_CreateSubscription_RejectsSecondCall(t *testing.T) {
	n, _ := g49NewNotifier()

	sub1, err := n.CreateSubscription()
	if err != nil {
		t.Fatalf("first CreateSubscription: %v", err)
	}
	if sub1.ID != ID("0x1") {
		t.Errorf("subscription ID = %v, want 0x1", sub1.ID)
	}

	if _, err := n.CreateSubscription(); !errors.Is(err, ErrMultipleSubscriptions) {
		t.Errorf("second CreateSubscription: got %v, want ErrMultipleSubscriptions", err)
	}

	n2, _ := g49NewNotifier()
	n2.takeSubscription() // marks callReturned=true with no subscription yet
	if _, err := n2.CreateSubscription(); !errors.Is(err, ErrSubscriptionAfterReturn) {
		t.Errorf("CreateSubscription after takeSubscription: got %v, want ErrSubscriptionAfterReturn", err)
	}
}

// TestNotifier_Notify_ErrorPaths covers the two early-return error
// branches in Notify that don't require a live connection.
func TestNotifier_Notify_ErrorPaths(t *testing.T) {
	n, _ := g49NewNotifier()

	if err := n.Notify(ID("0x1"), "x"); !errors.Is(err, ErrNotifyBeforeSubscription) {
		t.Errorf("Notify before subscription: got %v, want ErrNotifyBeforeSubscription", err)
	}

	sub, err := n.CreateSubscription()
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(ID("wrong-id"), "x"); !errors.Is(err, ErrNotifyWrongID) {
		t.Errorf("Notify with wrong ID: got %v, want ErrNotifyWrongID", err)
	}

	// Correct ID, not yet activated: buffered, no error.
	if err := n.Notify(sub.ID, "buffered-value"); err != nil {
		t.Errorf("Notify before activation: got %v, want nil (buffered)", err)
	}
}

// TestNotifier_ActivateFlushesBufferAndSends covers activate() draining
// the pre-activation buffer through send(), and Notify() going straight
// to send() once activated.
func TestNotifier_ActivateFlushesBufferAndSends(t *testing.T) {
	n, conn := g49NewNotifier()
	sub, err := n.CreateSubscription()
	if err != nil {
		t.Fatal(err)
	}

	if err := n.Notify(sub.ID, "one"); err != nil {
		t.Fatalf("Notify (buffered): %v", err)
	}
	if len(conn.written) != 0 {
		t.Fatalf("expected nothing written before activate, got %d messages", len(conn.written))
	}

	if err := n.activate(); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if len(conn.written) != 1 {
		t.Fatalf("expected the buffered notification flushed on activate, got %d messages", len(conn.written))
	}

	if err := n.Notify(sub.ID, "two"); err != nil {
		t.Fatalf("Notify (post-activation): %v", err)
	}
	if len(conn.written) != 2 {
		t.Fatalf("expected a second message written immediately post-activation, got %d", len(conn.written))
	}
}

// g49PubSubService is a minimal subscription-capable service used for
// the server-side end-to-end subscribe round trip. The method
// signature (ctx context.Context, ...) -> (*Subscription, error) is
// exactly what isPubSub requires to register it as a subscription
// callback (handleSubscribe's production entry point).
type g49PubSubService struct{}

func (s *g49PubSubService) Ticks(ctx context.Context, count int) (*Subscription, error) {
	notifier, supported := NotifierFromContext(ctx)
	if !supported {
		return nil, ErrNotificationsUnsupported
	}
	sub, err := notifier.CreateSubscription()
	if err != nil {
		return nil, err
	}
	go func() {
		for i := 0; i < count; i++ {
			_ = notifier.Notify(sub.ID, i)
		}
	}()
	return sub, nil
}

func newG49PubSubServerAndClient(t *testing.T) (*Server, *Client) {
	t.Helper()
	srv := NewServer()
	if err := srv.RegisterName("g49", &g49PubSubService{}); err != nil {
		t.Fatalf("RegisterName failed: %v", err)
	}
	client := DialInProc(srv)
	return srv, client
}

// TestSubscribe_RoundTripSucceeds drives the real handleSubscribe path
// over the in-process client/server pair: the "g49_subscribe" call
// must resolve to g49PubSubService.Ticks (found via the "ticks"
// subscription name), invoke CreateSubscription/Notify server-side,
// and return a non-nil ClientSubscription without error.
//
// NOTE (defect, not fixed — scope is test-only per task instructions):
// the returned *ClientSubscription's forwarding goroutine (sub.run(),
// subscription.go) is never started anywhere in production code — grep
// confirms no call site exists for ClientSubscription.run/deliver/close,
// and ClientSubscription.subid (needed by requestUnsubscribe) is never
// assigned either. So server->client notification delivery and
// sub.Unsubscribe() are both dead on arrival: Unsubscribe() would block
// forever (it waits on unsubDone, which only closes when run() exits).
// This test therefore verifies the subscribe round trip succeeds and
// stops there — it deliberately does NOT read from the subscription
// channel or call Unsubscribe(), to avoid hanging on this defect.
func TestSubscribe_RoundTripSucceeds(t *testing.T) {
	_, client := newG49PubSubServerAndClient(t)
	defer client.Close()

	ch := make(chan int, 16)
	sub, err := client.Subscribe(context.Background(), "g49", ch, "ticks", 3)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if sub == nil {
		t.Fatal("expected a non-nil ClientSubscription")
	}
}

// TestSubscribe_UnknownSubscriptionName covers handleSubscribe's
// subscriptionNotFoundError branch via a direct call (bypassing
// client.Subscribe, which requires a channel argument we don't need
// here since we expect an error response, not a live subscription).
func TestSubscribe_UnknownSubscriptionName(t *testing.T) {
	_, client := newG49PubSubServerAndClient(t)
	defer client.Close()

	var result ID
	err := client.CallContext(context.Background(), &result, "g49_subscribe", "doesNotExist")
	if err == nil {
		t.Fatal("expected an error for an unknown subscription name")
	}
}

// TestClientSubscribe_RejectsOnHTTPTransport covers the
// c.isHTTP branch of Client.Subscribe (ErrNotificationsUnsupported).
func TestClientSubscribe_RejectsOnHTTPTransport(t *testing.T) {
	client := &Client{isHTTP: true}
	ch := make(chan int, 1)
	_, err := client.Subscribe(context.Background(), "g49", ch, "ticks", 1)
	if !errors.Is(err, ErrNotificationsUnsupported) {
		t.Errorf("got %v, want ErrNotificationsUnsupported", err)
	}
}
