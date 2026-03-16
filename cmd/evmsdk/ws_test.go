package evmsdk

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestWebSocketServiceCloseNilConnections(t *testing.T) {
	ws := &WebSocketService{done: make(chan struct{})}

	if err := ws.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case <-ws.done:
	default:
		t.Fatal("Close() did not close done channel")
	}
}

func TestWebSocketServiceForwardWritesStopsOnClose(t *testing.T) {
	ws := &WebSocketService{done: make(chan struct{})}
	in := make(chan []byte)
	exited := make(chan struct{})
	var sends atomic.Int32

	go func() {
		defer close(exited)
		ws.forwardWrites(in, func([]byte) error {
			sends.Add(1)
			return nil
		})
	}()

	if err := ws.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("forwardWrites() did not exit after Close()")
	}

	if sends.Load() != 0 {
		t.Fatalf("forwardWrites() send count = %d, want 0", sends.Load())
	}
}

func TestWebSocketServiceForwardWritesDeliversWrappedRequest(t *testing.T) {
	ws := &WebSocketService{done: make(chan struct{})}
	in := make(chan []byte, 1)
	in <- []byte(`{"number":1}`)

	wrapped := make(chan []byte, 1)
	exited := make(chan struct{})

	go func() {
		defer close(exited)
		ws.forwardWrites(in, func(msg []byte) error {
			wrapped <- msg
			_ = ws.Close()
			return nil
		})
	}()

	select {
	case msg := <-wrapped:
		if string(msg) != `{"jsonrpc":"2.0","method":"eth_submitSign","id":1,"params":[{"number":1}]}` {
			t.Fatalf("wrapped request = %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("forwardWrites() did not deliver wrapped request")
	}

	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("forwardWrites() did not exit after Close() from send callback")
	}
}

func TestWebSocketServiceForwardReadMessageStopsOnClose(t *testing.T) {
	ws := &WebSocketService{done: make(chan struct{})}
	out := make(chan []byte)
	done := make(chan bool, 1)

	go func() {
		done <- ws.forwardReadMessage(out, []byte("message"))
	}()

	if err := ws.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case ok := <-done:
		if ok {
			t.Fatal("forwardReadMessage() = true, want false after Close()")
		}
	case <-time.After(time.Second):
		t.Fatal("forwardReadMessage() blocked after Close()")
	}
}

func TestWebSocketServiceForwardReadMessageDelivers(t *testing.T) {
	ws := &WebSocketService{done: make(chan struct{})}
	out := make(chan []byte, 1)

	if ok := ws.forwardReadMessage(out, []byte("message")); !ok {
		t.Fatal("forwardReadMessage() = false, want true")
	}

	select {
	case got := <-out:
		if string(got) != "message" {
			t.Fatalf("got %q, want %q", got, "message")
		}
	case <-time.After(time.Second):
		t.Fatal("forwardReadMessage() did not deliver message")
	}
}
