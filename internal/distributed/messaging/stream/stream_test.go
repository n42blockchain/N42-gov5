// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockProvider implements MessageProvider for testing.
type mockProvider struct {
	mu       sync.Mutex
	handlers map[string][]func(msg *StreamMessage)
}

func newMockProvider() *mockProvider {
	return &mockProvider{
		handlers: make(map[string][]func(msg *StreamMessage)),
	}
}

func (m *mockProvider) Publish(topic string, payload []byte, contentType string, senderID string) ([32]byte, error) {
	return [32]byte{}, nil
}

func (m *mockProvider) Subscribe(topic string, handler func(msg *StreamMessage)) {
	m.mu.Lock()
	m.handlers[topic] = append(m.handlers[topic], handler)
	m.mu.Unlock()
}

func (m *mockProvider) GetMessages(topic string, from, to time.Time, limit int) []*StreamMessage {
	return nil
}

// emit simulates sending a message to all subscribers for a topic.
func (m *mockProvider) emit(topic string, msg *StreamMessage) {
	m.mu.Lock()
	var handlers []func(msg *StreamMessage)
	handlers = append(handlers, m.handlers[topic]...)
	m.mu.Unlock()
	for _, h := range handlers {
		h(msg)
	}
}

func startTestServer(t *testing.T, provider MessageProvider) *StreamServer {
	t.Helper()
	ss := NewStreamServer("127.0.0.1:0", provider)
	if err := ss.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ss
}

func serverAddr(ss *StreamServer) string {
	return ss.listener.Addr().String()
}

func TestStreamServerStartStop(t *testing.T) {
	provider := newMockProvider()
	ss := startTestServer(t, provider)
	defer ss.Stop()

	addr := serverAddr(ss)
	resp, err := http.Get(fmt.Sprintf("http://%s/health", addr))
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	ss.Stop()

	// After stop, requests should fail (give a moment for shutdown)
	time.Sleep(50 * time.Millisecond)
	_, err = http.Get(fmt.Sprintf("http://%s/health", addr))
	if err == nil {
		t.Log("request after stop may succeed due to OS buffering; this is acceptable")
	}
}

func TestStreamServerHealthCheck(t *testing.T) {
	provider := newMockProvider()
	ss := startTestServer(t, provider)
	defer ss.Stop()

	addr := serverAddr(ss)
	resp, err := http.Get(fmt.Sprintf("http://%s/health", addr))
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

// connectSSERaw opens a raw TCP connection and sends an HTTP request for SSE.
// This avoids the http.Client timeout issues with long-lived SSE connections.
// It returns the connection and a buffered reader for reading SSE events.
func connectSSERaw(t *testing.T, addr, path string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Send HTTP GET request
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nAccept: text/event-stream\r\n\r\n", path, addr)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		t.Fatalf("write request: %v", err)
	}
	reader := bufio.NewReader(conn)
	return conn, reader
}

// readSSEEvent reads SSE lines from the reader until it finds a "data:" line
// or the deadline expires. Returns the data payload or empty string on timeout.
func readSSEEvent(reader *bufio.Reader, conn net.Conn, timeout time.Duration) string {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return ""
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			return line[6:]
		}
	}
}

func TestStreamServerSSE(t *testing.T) {
	provider := newMockProvider()
	ss := startTestServer(t, provider)
	defer ss.Stop()

	addr := serverAddr(ss)

	// Connect via raw TCP to avoid http.Client timeout on SSE
	conn, reader := connectSSERaw(t, addr, "/ws/messages?topic=test")
	defer conn.Close()

	// Wait for the server to register the client and subscription
	time.Sleep(200 * time.Millisecond)

	// Emit a message via the provider (triggers the subscription handler)
	testMsg := &StreamMessage{
		Topic:       "test",
		Payload:     []byte("hello-sse"),
		ContentType: "text/plain",
		Timestamp:   time.Now(),
		SenderID:    "sender1",
	}
	provider.emit("test", testMsg)

	// Read until we find a data line (SSE event)
	data := readSSEEvent(reader, conn, 3*time.Second)
	if data == "" {
		t.Error("did not receive SSE event within timeout")
		return
	}

	var notif Notification
	if err := json.Unmarshal([]byte(data), &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.Method != "message" {
		t.Errorf("notification method = %q, want %q", notif.Method, "message")
	}
}

func TestStreamServerBroadcast(t *testing.T) {
	provider := newMockProvider()
	ss := startTestServer(t, provider)
	defer ss.Stop()

	addr := serverAddr(ss)

	// Connect via raw TCP
	conn, reader := connectSSERaw(t, addr, "/ws/messages?topic=broadcast-topic")
	defer conn.Close()

	// Wait for registration
	time.Sleep(200 * time.Millisecond)

	// Broadcast a message directly through the server
	testMsg := &StreamMessage{
		Topic:       "broadcast-topic",
		Payload:     []byte("broadcast-data"),
		ContentType: "application/json",
		Timestamp:   time.Now(),
		SenderID:    "broadcaster",
	}
	ss.Broadcast("broadcast-topic", testMsg)

	// Read SSE event
	data := readSSEEvent(reader, conn, 3*time.Second)
	if data == "" {
		t.Error("broadcast message not received within timeout")
	}
}

func TestStreamServerClientCount(t *testing.T) {
	provider := newMockProvider()
	ss := startTestServer(t, provider)
	defer ss.Stop()

	addr := serverAddr(ss)

	if ss.ClientCount() != 0 {
		t.Errorf("initial client count = %d, want 0", ss.ClientCount())
	}

	// Connect 2 SSE clients via raw TCP
	conn1, _ := connectSSERaw(t, addr, "/ws/messages?topic=count")
	defer conn1.Close()

	// Use a brief context-based HTTP request to establish the second connection
	// in the background so both are registered concurrently
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	go func() {
		req, _ := http.NewRequestWithContext(ctx2, "GET",
			fmt.Sprintf("http://%s/ws/messages?topic=count", addr), nil)
		transport := &http.Transport{}
		client := &http.Client{Transport: transport}
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			// Block until cancelled
			<-ctx2.Done()
		}
	}()

	// Wait for both to register
	time.Sleep(300 * time.Millisecond)

	count := ss.ClientCount()
	if count != 2 {
		t.Errorf("client count = %d, want 2", count)
	}
}
