// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package stream provides a WebSocket streaming server for real-time
// message subscriptions, following JSON-RPC patterns.
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/log"
)

const (
	// writeWait is the time allowed to write a message to the client.
	writeWait = 10 * time.Second

	// readWait is the time to wait for a read before timing out.
	readWait = 60 * time.Second

	// pingPeriod sends pings at this interval (must be < readWait).
	pingPeriod = 30 * time.Second

	// maxWSMessageSize is the maximum WebSocket message size.
	maxWSMessageSize = 1 << 20 // 1 MiB
)

// MessageProvider is the interface for message publishing and querying.
type MessageProvider interface {
	Publish(topic string, payload []byte, contentType string, senderID string) ([32]byte, error)
	Subscribe(topic string, handler func(msg *StreamMessage))
	GetMessages(topic string, from, to time.Time, limit int) []*StreamMessage
}

// StreamMessage represents a message in the streaming API.
type StreamMessage struct {
	ID          [32]byte  `json:"id"`
	Topic       string    `json:"topic"`
	Payload     []byte    `json:"payload"`
	ContentType string    `json:"contentType"`
	Timestamp   time.Time `json:"timestamp"`
	SenderID    string    `json:"senderID"`
}

// Request is a JSON-RPC style request.
type Request struct {
	Method string          `json:"method"`
	ID     uint64          `json:"id"`
	Params json.RawMessage `json:"params"`
}

// Response is a JSON-RPC style response.
type Response struct {
	ID     uint64      `json:"id,omitempty"`
	Result interface{} `json:"result,omitempty"`
	Error  *ErrorObj   `json:"error,omitempty"`
}

// Notification is a server-sent event.
type Notification struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

// ErrorObj represents a JSON-RPC error.
type ErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// SubscribeParams defines subscription parameters.
type SubscribeParams struct {
	Topics  []string `json:"topics"`
	Filters *Filter  `json:"filters,omitempty"`
}

// Filter defines message filters for subscriptions.
type Filter struct {
	ContentType string `json:"contentType,omitempty"`
	SenderID    string `json:"senderID,omitempty"`
}

// PublishParams defines publish parameters.
type PublishParams struct {
	Topic       string `json:"topic"`
	Payload     []byte `json:"payload"`
	ContentType string `json:"contentType"`
}

// QueryParams defines query parameters.
type QueryParams struct {
	Topic string `json:"topic"`
	From  int64  `json:"from"` // unix nano
	To    int64  `json:"to"`   // unix nano
	Limit int    `json:"limit"`
}

// StreamServer manages WebSocket connections for real-time message streaming.
type StreamServer struct {
	addr     string
	provider MessageProvider
	server   http.Server

	clients  sync.Map // connID -> *client
	nextID   atomic.Uint64
	ctx      context.Context
	cancel   context.CancelFunc
	listener net.Listener
}

// client represents a connected streaming client.
type client struct {
	id      uint64
	server  *StreamServer
	topics  map[string]bool
	filters *Filter
	send    chan []byte
	done    chan struct{}
}

// NewStreamServer creates a new streaming server.
func NewStreamServer(addr string, provider MessageProvider) *StreamServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &StreamServer{
		addr:     addr,
		provider: provider,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start begins listening for WebSocket connections.
func (ss *StreamServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/messages", ss.handleWebSocket)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	var err error
	ss.listener, err = net.Listen("tcp", ss.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	ss.server = http.Server{
		Handler:      mux,
		ReadTimeout:  readWait,
		WriteTimeout: writeWait,
	}

	go func() {
		if err := ss.server.Serve(ss.listener); err != nil && err != http.ErrServerClosed {
			log.Error("Stream server error", "err", err)
		}
	}()

	log.Info("Message stream server started", "addr", ss.addr)
	return nil
}

// Stop gracefully shuts down the server.
func (ss *StreamServer) Stop() {
	ss.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ss.server.Shutdown(ctx)

	// Client handlers already select on ss.ctx.Done() which was cancelled above,
	// so they will exit on their own. No need to close c.done here, which would
	// risk a double-close panic if the client is already disconnecting.

	log.Info("Message stream server stopped")
}

// handleWebSocket upgrades HTTP to a Server-Sent Events (SSE) stream.
// Uses SSE as a simpler alternative to WebSocket that requires no external
// dependencies. For full WebSocket support, integrate with the existing
// gorilla/websocket from the RPC layer.
func (ss *StreamServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	clientID := ss.nextID.Add(1)

	// Parse topic subscriptions from query params BEFORE storing the client
	// so that Broadcast cannot race on c.topics.
	topicParams := r.URL.Query()["topic"]
	topicSet := make(map[string]bool, len(topicParams))
	for _, t := range topicParams {
		topicSet[t] = true
	}

	c := &client{
		id:     clientID,
		server: ss,
		topics: topicSet,
		send:   make(chan []byte, 256),
		done:   make(chan struct{}),
	}
	ss.clients.Store(clientID, c)

	defer func() {
		ss.clients.Delete(clientID)
	}()

	// Subscribe to topics for push notifications
	for _, t := range topicParams {
		topicCopy := t
		ss.provider.Subscribe(topicCopy, func(msg *StreamMessage) {
			data, err := json.Marshal(&Notification{
				Method: "message",
				Params: msg,
			})
			if err != nil {
				return
			}
			select {
			case c.send <- data:
			default:
				// Client too slow, skip message
			}
		})
	}

	log.Debug("SSE client connected", "id", clientID, "topics", topicParams)

	// SSE write loop
	for {
		select {
		case <-ss.ctx.Done():
			return
		case <-c.done:
			return
		case <-r.Context().Done():
			return
		case data, ok := <-c.send:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// Broadcast sends a message to all subscribed clients.
func (ss *StreamServer) Broadcast(topic string, msg *StreamMessage) {
	data, err := json.Marshal(&Notification{
		Method: "message",
		Params: msg,
	})
	if err != nil {
		return
	}

	ss.clients.Range(func(_, value interface{}) bool {
		c := value.(*client)
		if c.topics[topic] {
			select {
			case c.send <- data:
			default:
			}
		}
		return true
	})
}

// ClientCount returns the number of connected clients.
func (ss *StreamServer) ClientCount() int {
	count := 0
	ss.clients.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}
