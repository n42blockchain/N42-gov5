// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package packetd

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	log "github.com/n42blockchain/N42/lib/log/v3"
)

// HeadSubscriber subscribes to a node's newHeads over a WebSocket
// (eth_subscribe) and signals a wake channel on every head, so the follower
// builds a block's packet as soon as the node announces it instead of at its
// next head poll. It reconnects after a failure; the follower's poll stays
// the fallback and the catch-up path, so a lost or late notification costs at
// most one poll interval, never a block.
type HeadSubscriber struct {
	URL    string
	Dialer *websocket.Dialer // nil: websocket.DefaultDialer
	// Backoff is the delay before a reconnect (default 1s).
	Backoff time.Duration

	heads    atomic.Uint64
	connects atomic.Uint64
}

// Heads returns the number of newHeads notifications received.
func (s *HeadSubscriber) Heads() uint64 { return s.heads.Load() }

// Connects returns the number of subscriptions established.
func (s *HeadSubscriber) Connects() uint64 { return s.connects.Load() }

// Run keeps a newHeads subscription open until ctx ends, sending on wake
// (without blocking: one pending wake is enough) for every head.
func (s *HeadSubscriber) Run(ctx context.Context, wake chan<- struct{}) {
	backoff := s.Backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	for {
		err := s.subscribe(ctx, wake)
		if ctx.Err() != nil {
			return
		}
		log.Warn("packetd: newHeads subscription lost, polling until it is back", "ws", s.URL, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

type subscriptionMessage struct {
	ID     *uint64          `json:"id"`
	Result json.RawMessage  `json:"result"`
	Error  *RPCError        `json:"error"`
	Method string           `json:"method"`
	Params *subscriptionArg `json:"params"`
}

type subscriptionArg struct {
	Subscription string `json:"subscription"`
}

func (s *HeadSubscriber) subscribe(ctx context.Context, wake chan<- struct{}) error {
	d := s.Dialer
	if d == nil {
		d = websocket.DefaultDialer
	}
	conn, resp, err := d.DialContext(ctx, s.URL, nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "eth_subscribe", "params": []any{"newHeads"}}
	if err := conn.WriteJSON(req); err != nil {
		return fmt.Errorf("eth_subscribe: %w", err)
	}
	var subID string
	for {
		var m subscriptionMessage
		if err := conn.ReadJSON(&m); err != nil {
			return fmt.Errorf("read: %w", err)
		}
		switch {
		case m.ID != nil && *m.ID == 1:
			if m.Error != nil {
				return fmt.Errorf("eth_subscribe: %w", m.Error)
			}
			if err := json.Unmarshal(m.Result, &subID); err != nil || subID == "" {
				return fmt.Errorf("eth_subscribe: unexpected result %s", m.Result)
			}
			s.connects.Add(1)
			log.Info("packetd: subscribed to newHeads", "ws", s.URL, "id", subID)
		case m.Method == "eth_subscription" && m.Params != nil && subID != "" && m.Params.Subscription == subID:
			s.heads.Add(1)
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
}
