// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package notify implements a push notification service that monitors
// smart contract events and routes notifications to subscribed clients.
package notify

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
)

// Notification represents a single push notification.
type Notification struct {
	ID          types.Hash    `json:"id"`
	Address     types.Address `json:"address"`
	Topic       types.Hash    `json:"topic"`
	Data        []byte        `json:"data"`
	BlockNumber uint64        `json:"blockNumber"`
	TxHash      types.Hash    `json:"txHash"`
	Timestamp   time.Time     `json:"timestamp"`
}

// SubscriptionFilter specifies which events a client wants to receive.
type SubscriptionFilter struct {
	Addresses []types.Address `json:"addresses"`
	Topics    []types.Hash    `json:"topics"`
}

// Channel is a single subscriber's notification stream.
type Channel struct {
	id     string
	filter SubscriptionFilter
	ch     chan *Notification
	closed atomic.Bool
}

// NewChannel creates a notification channel.
func NewChannel(id string, filter SubscriptionFilter, bufferSize int) *Channel {
	return &Channel{
		id:     id,
		filter: filter,
		ch:     make(chan *Notification, bufferSize),
	}
}

// Send delivers a notification. Non-blocking: returns false if channel full.
func (c *Channel) Send(n *Notification) bool {
	if c.closed.Load() {
		return false
	}
	select {
	case c.ch <- n:
		return true
	default:
		return false
	}
}

// Receive returns the notification receive channel.
func (c *Channel) Receive() <-chan *Notification {
	return c.ch
}

// Close closes the channel.
func (c *Channel) Close() {
	if c.closed.CompareAndSwap(false, true) {
		close(c.ch)
	}
}

// Matches checks if a notification matches this channel's filter.
func (c *Channel) Matches(n *Notification) bool {
	if len(c.filter.Addresses) > 0 {
		found := false
		for _, addr := range c.filter.Addresses {
			if addr == n.Address {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(c.filter.Topics) > 0 {
		found := false
		for _, topic := range c.filter.Topics {
			if topic == n.Topic {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Service manages notification channels and dispatches events.
type Service struct {
	cfg      *conf.NotifyCfg
	channels map[string]*Channel
	history  map[types.Address][]*Notification

	mu       sync.RWMutex
	nextID   atomic.Uint64
	sent     atomic.Uint64
	dropped  atomic.Uint64

	once sync.Once
}

// NewService creates a notification service.
func NewService(cfg *conf.NotifyCfg) *Service {
	return &Service{
		cfg:      cfg,
		channels: make(map[string]*Channel),
		history:  make(map[types.Address][]*Notification),
	}
}

// Start begins the notification service.
func (s *Service) Start() {
	log.Info("Notification service started",
		"maxHistory", s.cfg.MaxHistory,
		"maxSubscribers", s.cfg.MaxSubscribers,
		"bufferSize", s.cfg.BufferSize,
	)
}

// Stop shuts down the service and closes all channels.
func (s *Service) Stop() {
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, ch := range s.channels {
			ch.Close()
		}
		log.Info("Notification service stopped",
			"sent", s.sent.Load(),
			"dropped", s.dropped.Load(),
			"subscribers", len(s.channels),
		)
	})
}

// Subscribe creates a new notification channel.
func (s *Service) Subscribe(filter SubscriptionFilter) (*Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.channels) >= s.cfg.MaxSubscribers {
		return nil, ErrMaxSubscribers
	}
	id := s.nextID.Add(1)
	chID := types.Bytes2Hex([]byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)})
	ch := NewChannel(chID, filter, s.cfg.BufferSize)
	s.channels[chID] = ch
	return ch, nil
}

// Unsubscribe removes a channel.
func (s *Service) Unsubscribe(channelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.channels[channelID]; ok {
		ch.Close()
		delete(s.channels, channelID)
	}
}

// Dispatch sends a notification to all matching subscribers and records history.
func (s *Service) Dispatch(n *Notification) {
	// Fan out to subscribers under read lock (Send is non-blocking).
	s.mu.RLock()
	for _, ch := range s.channels {
		if ch.Matches(n) {
			if ch.Send(n) {
				s.sent.Add(1)
			} else {
				s.dropped.Add(1)
			}
		}
	}
	s.mu.RUnlock()

	// Record history under write lock.
	s.mu.Lock()
	maxAddrs := s.cfg.MaxHistoryAddresses
	if maxAddrs <= 0 {
		maxAddrs = 10000
	}
	hist := s.history[n.Address]
	if hist != nil || len(s.history) < maxAddrs {
		if len(hist) >= s.cfg.MaxHistory {
			newHist := make([]*Notification, len(hist)-1, s.cfg.MaxHistory)
			copy(newHist, hist[1:])
			hist = newHist
		}
		s.history[n.Address] = append(hist, n)
	}
	s.mu.Unlock()
}

// History returns recent notifications for an address.
func (s *Service) History(address types.Address, limit int) []*Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()
	hist := s.history[address]
	if limit <= 0 || limit > len(hist) {
		limit = len(hist)
	}
	// Return most recent
	start := len(hist) - limit
	result := make([]*Notification, limit)
	copy(result, hist[start:])
	return result
}

// Stats returns service statistics.
func (s *Service) Stats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]interface{}{
		"subscribers": len(s.channels),
		"sent":        s.sent.Load(),
		"dropped":     s.dropped.Load(),
	}
}

// ChannelCount returns the number of active subscribers.
func (s *Service) ChannelCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.channels)
}

// HistoryCount returns the number of addresses with notification history.
func (s *Service) HistoryCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.history)
}

// ClearHistory removes all notification history.
func (s *Service) ClearHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = make(map[types.Address][]*Notification)
}
