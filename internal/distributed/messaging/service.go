// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package messaging implements a decentralized message relay service using
// libp2p pubsub, providing Waku-compatible application-layer messaging
// alongside the consensus P2P network.
package messaging

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
)

// funcPointer returns the raw pointer of a function value for comparison.
func funcPointer(fn MessageHandler) uintptr {
	return reflect.ValueOf(fn).Pointer()
}

// Message represents a single relay message.
type Message struct {
	ID          types.Hash `json:"id"`
	Topic       string     `json:"topic"`
	Payload     []byte     `json:"payload"`
	ContentType string     `json:"contentType"`
	Timestamp   time.Time  `json:"timestamp"`
	SenderID    string     `json:"senderID,omitempty"`
}

// MessageHandler is called when a message is received on a subscribed topic.
type MessageHandler func(msg *Message)

// Store provides LRU message storage with TTL-based expiry.
type Store struct {
	mu       sync.RWMutex
	messages map[types.Hash]*Message
	byTopic  map[string][]*Message
	capacity int
	ttl      time.Duration
}

// NewStore creates a message store.
func NewStore(capacity int, ttl time.Duration) *Store {
	return &Store{
		messages: make(map[types.Hash]*Message),
		byTopic:  make(map[string][]*Message),
		capacity: capacity,
		ttl:      ttl,
	}
}

// Add stores a message.
func (s *Store) Add(msg *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) >= s.capacity {
		s.evictOldest()
	}
	s.messages[msg.ID] = msg
	s.byTopic[msg.Topic] = append(s.byTopic[msg.Topic], msg)
}

// Get retrieves a message by ID.
func (s *Store) Get(id types.Hash) (*Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.messages[id]
	return m, ok
}

// GetByTopic returns messages for a topic within a time range.
func (s *Store) GetByTopic(topic string, from, to time.Time, limit int) []*Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := s.byTopic[topic]
	var result []*Message
	for i := len(msgs) - 1; i >= 0 && len(result) < limit; i-- {
		m := msgs[i]
		if (from.IsZero() || !m.Timestamp.Before(from)) && (to.IsZero() || !m.Timestamp.After(to)) {
			result = append(result, m)
		}
	}
	return result
}

// Prune removes messages older than TTL.
func (s *Store) Prune() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-s.ttl)
	pruned := 0
	for id, m := range s.messages {
		if m.Timestamp.Before(cutoff) {
			delete(s.messages, id)
			pruned++
		}
	}
	// Rebuild topic indices, removing empty topics to avoid map leak.
	for topic := range s.byTopic {
		filtered := s.byTopic[topic][:0]
		for _, m := range s.byTopic[topic] {
			if _, ok := s.messages[m.ID]; ok {
				filtered = append(filtered, m)
			}
		}
		if len(filtered) == 0 {
			delete(s.byTopic, topic)
		} else {
			s.byTopic[topic] = filtered
		}
	}
	return pruned
}

func (s *Store) evictOldest() {
	var oldestID types.Hash
	var oldestTime time.Time
	first := true
	for id, m := range s.messages {
		if first || m.Timestamp.Before(oldestTime) {
			oldestID = id
			oldestTime = m.Timestamp
			first = false
		}
	}
	if !first {
		oldMsg := s.messages[oldestID]
		delete(s.messages, oldestID)
		// Remove from topic index to prevent stale entries accumulating
		// between Prune() cycles.
		if oldMsg != nil {
			topic := oldMsg.Topic
			msgs := s.byTopic[topic]
			for i, m := range msgs {
				if m.ID == oldestID {
					s.byTopic[topic] = append(msgs[:i], msgs[i+1:]...)
					break
				}
			}
			if len(s.byTopic[topic]) == 0 {
				delete(s.byTopic, topic)
			}
		}
	}
}

// Size returns the current store size.
func (s *Store) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// RateLimiter implements per-sender sliding window rate limiting.
type RateLimiter struct {
	mu        sync.Mutex
	windows   map[string][]time.Time
	rateLimit int
	windowLen time.Duration
}

// NewRateLimiter creates a rate limiter.
func NewRateLimiter(rateLimit int) *RateLimiter {
	return &RateLimiter{
		windows:   make(map[string][]time.Time),
		rateLimit: rateLimit,
		windowLen: time.Minute,
	}
}

// Allow checks if a sender is allowed to send a message.
func (rl *RateLimiter) Allow(senderID string) bool {
	if rl.rateLimit <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.windowLen)

	// Clean old entries
	window := rl.windows[senderID]
	cleaned := window[:0]
	for _, t := range window {
		if t.After(cutoff) {
			cleaned = append(cleaned, t)
		}
	}
	if len(cleaned) >= rl.rateLimit {
		rl.windows[senderID] = cleaned
		return false
	}
	rl.windows[senderID] = append(cleaned, now)
	return true
}

// PruneStale removes sender windows that have fully expired,
// preventing unbounded growth of the windows map.
func (rl *RateLimiter) PruneStale() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-rl.windowLen)
	for sender, window := range rl.windows {
		allExpired := true
		for _, t := range window {
			if t.After(cutoff) {
				allExpired = false
				break
			}
		}
		if allExpired {
			delete(rl.windows, sender)
		}
	}
}

// Service is the main messaging relay service.
type Service struct {
	cfg         *conf.MessagingCfg
	store       *Store
	rateLimiter *RateLimiter
	handlers    map[string][]MessageHandler
	relay       *Relay

	mu          sync.RWMutex
	published   atomic.Uint64
	received    atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// Agent discovery and negotiation topic constants.
const (
	AgentDiscoveryTopic  = "/n42/agents/discovery"
	AgentNegotiateTopic  = "/n42/agents/negotiate"
)

// NewService creates a messaging service.
func NewService(cfg *conf.MessagingCfg) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		cfg:         cfg,
		store:       NewStore(cfg.StoreCapacity, time.Duration(cfg.StoreTTLSec)*time.Second),
		rateLimiter: NewRateLimiter(cfg.RLNRateLimit),
		handlers:    make(map[string][]MessageHandler),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Start launches the messaging service background workers.
func (s *Service) Start() {
	s.wg.Add(1)
	go s.pruneLoop()
	log.Info("Messaging service started",
		"storeCapacity", s.cfg.StoreCapacity,
		"ttl", s.cfg.StoreTTLSec,
		"rateLimit", s.cfg.RLNRateLimit,
	)
}

// Stop gracefully shuts down the service.
func (s *Service) Stop() {
	s.once.Do(func() {
		s.cancel()
		s.wg.Wait()
		log.Info("Messaging service stopped",
			"published", s.published.Load(),
			"received", s.received.Load(),
			"storeSize", s.store.Size(),
		)
	})
}

// Publish sends a message on a topic.
func (s *Service) Publish(topic string, payload []byte, contentType string, senderID string) (types.Hash, error) {
	if len(payload) > s.cfg.MaxMessageSize {
		return types.Hash{}, fmt.Errorf("message too large: %d > %d", len(payload), s.cfg.MaxMessageSize)
	}
	if !s.rateLimiter.Allow(senderID) {
		return types.Hash{}, fmt.Errorf("rate limit exceeded for sender")
	}

	// Length-prefixed hash to prevent (topic,payload) collision across boundaries
	idHasher := sha256.New()
	topicBytes := []byte(topic)
	idHasher.Write([]byte{byte(len(topicBytes) >> 8), byte(len(topicBytes))})
	idHasher.Write(topicBytes)
	idHasher.Write(payload)
	idHasher.Write([]byte(senderID))
	var h [32]byte
	copy(h[:], idHasher.Sum(nil))
	msg := &Message{
		ID:          types.Hash(h),
		Topic:       topic,
		Payload:     payload,
		ContentType: contentType,
		Timestamp:   time.Now(),
		SenderID:    senderID,
	}
	s.store.Add(msg)
	s.published.Add(1)

	// Dispatch to handlers
	s.mu.RLock()
	handlers := s.handlers[topic]
	s.mu.RUnlock()
	for _, h := range handlers {
		h(msg)
	}

	// Forward to P2P relay if available
	if s.relay != nil {
		if err := s.relay.PublishToNetwork(msg); err != nil {
			log.Debug("Failed to relay message", "err", err)
		}
	}

	return msg.ID, nil
}

// Subscribe registers a handler for a topic.
func (s *Service) Subscribe(topic string, handler MessageHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[topic] = append(s.handlers[topic], handler)
}

// Unsubscribe removes a handler from a topic by pointer equality.
// This prevents handler list growth when subscribers are repeatedly added.
func (s *Service) Unsubscribe(topic string, handler MessageHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	handlers := s.handlers[topic]
	targetPtr := funcPointer(handler)
	for i, h := range handlers {
		if funcPointer(h) == targetPtr {
			s.handlers[topic] = append(handlers[:i], handlers[i+1:]...)
			if len(s.handlers[topic]) == 0 {
				delete(s.handlers, topic)
			}
			return
		}
	}
}

// GetMessages retrieves stored messages by topic.
func (s *Service) GetMessages(topic string, from, to time.Time, limit int) []*Message {
	return s.store.GetByTopic(topic, from, to, limit)
}

// Stats returns service statistics.
func (s *Service) Stats() map[string]interface{} {
	return map[string]interface{}{
		"published": s.published.Load(),
		"received":  s.received.Load(),
		"storeSize": s.store.Size(),
	}
}

// SetRelay attaches a P2P relay to the service for cross-node message propagation.
func (s *Service) SetRelay(r *Relay) {
	s.relay = r
}

// DeliverFromRelay processes a message received from the P2P relay.
// It stores the message and dispatches to local handlers without re-relaying.
func (s *Service) DeliverFromRelay(msg *Message) {
	s.store.Add(msg)
	s.received.Add(1)

	s.mu.RLock()
	handlers := s.handlers[msg.Topic]
	s.mu.RUnlock()
	for _, h := range handlers {
		h(msg)
	}
}

func (s *Service) pruneLoop() {
	defer s.wg.Done()
	interval := time.Duration(s.cfg.StoreTTLSec/2) * time.Second
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.store.Prune()
			s.rateLimiter.PruneStale()
		}
	}
}
