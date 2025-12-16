// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package p2p

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
)

// =============================================================================
// Sync-specific P2P Interface
// =============================================================================

// PeerID is an alias for peer identification.
type PeerID = peer.ID

// SyncP2P defines the minimal P2P interface required by the sync package.
// This interface decouples sync logic from the concrete P2P implementation,
// enabling easier testing and future alternative implementations.
type SyncP2P interface {
	// PeerProvider provides access to connected peers
	PeerProvider

	// BlockRequester provides block fetching capabilities
	BlockRequester

	// TopicSubscriber provides pub/sub subscription capabilities
	TopicSubscriber

	// PeerScorer provides peer scoring capabilities
	PeerScorer
}

// PeerProvider abstracts peer management for sync operations.
type PeerProvider interface {
	// ConnectedPeers returns the list of currently connected peer IDs.
	ConnectedPeers() []PeerID

	// BestPeers returns the best peers for syncing based on their reported block height.
	// Returns the highest known block number and the peer IDs.
	BestPeers(minCount int, currentBlock *uint256.Int) (*uint256.Int, []PeerID)

	// PeerCount returns the number of connected peers.
	PeerCount() int

	// IsPeerConnected checks if a specific peer is connected.
	IsPeerConnected(pid PeerID) bool
}

// BlockRequester abstracts block fetching operations.
type BlockRequester interface {
	// RequestBlocksByRange requests a range of blocks from a specific peer.
	// Returns the blocks and any error encountered.
	RequestBlocksByRange(ctx context.Context, pid PeerID, start *uint256.Int, count uint64) ([][]byte, error)

	// RequestBlocksByHash requests specific blocks by their hashes.
	RequestBlocksByHash(ctx context.Context, pid PeerID, hashes [][]byte) ([][]byte, error)
}

// TopicSubscriber abstracts pub/sub subscription for sync-related topics.
type TopicSubscriber interface {
	// SubscribeToBlocks subscribes to new block announcements.
	// Returns an unsubscribe function and any error.
	SubscribeToBlocks(handler func(data []byte, from PeerID) error) (unsubscribe func(), err error)

	// SubscribeToTxs subscribes to new transaction announcements.
	SubscribeToTxs(handler func(data []byte, from PeerID) error) (unsubscribe func(), err error)
}

// PeerScorer abstracts peer scoring and reputation management.
type PeerScorer interface {
	// IncrementPeerScore increases a peer's score after successful interaction.
	IncrementPeerScore(pid PeerID, delta int64)

	// DecrementPeerScore decreases a peer's score after failed interaction.
	DecrementPeerScore(pid PeerID, delta int64)

	// GetPeerScore returns the current score of a peer.
	GetPeerScore(pid PeerID) int64

	// BanPeer temporarily or permanently bans a peer.
	BanPeer(pid PeerID, duration time.Duration, reason string)
}

// =============================================================================
// P2P Metrics
// =============================================================================

// P2PMetrics collects P2P-related metrics for sync operations.
type P2PMetrics struct {
	mu sync.RWMutex

	// Peer metrics
	peersConnected    int64
	peersDisconnected int64
	peersBanned       int64

	// Request metrics
	requestsTotal   uint64
	requestsFailed  uint64
	requestLatency  []time.Duration
	lastRequestTime time.Time

	// Block metrics
	blocksReceived uint64
	bytesReceived  uint64
}

// NewP2PMetrics creates a new P2PMetrics instance.
func NewP2PMetrics() *P2PMetrics {
	return &P2PMetrics{
		requestLatency: make([]time.Duration, 0, 1000),
	}
}

// RecordPeerConnect records a peer connection event.
func (m *P2PMetrics) RecordPeerConnect() {
	atomic.AddInt64(&m.peersConnected, 1)
}

// RecordPeerDisconnect records a peer disconnection event.
func (m *P2PMetrics) RecordPeerDisconnect() {
	atomic.AddInt64(&m.peersDisconnected, 1)
}

// RecordPeerBan records a peer ban event.
func (m *P2PMetrics) RecordPeerBan() {
	atomic.AddInt64(&m.peersBanned, 1)
}

// RecordRequest records a block request with its latency and success status.
func (m *P2PMetrics) RecordRequest(latency time.Duration, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requestsTotal++
	if !success {
		m.requestsFailed++
	}
	m.lastRequestTime = time.Now()

	// Keep last 1000 latencies for averaging
	if len(m.requestLatency) >= 1000 {
		m.requestLatency = m.requestLatency[1:]
	}
	m.requestLatency = append(m.requestLatency, latency)
}

// RecordBlocksReceived records blocks received from peers.
func (m *P2PMetrics) RecordBlocksReceived(count uint64, bytes uint64) {
	atomic.AddUint64(&m.blocksReceived, count)
	atomic.AddUint64(&m.bytesReceived, bytes)
}

// RequestFailureRate returns the request failure rate.
func (m *P2PMetrics) RequestFailureRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.requestsTotal == 0 {
		return 0
	}
	return float64(m.requestsFailed) / float64(m.requestsTotal)
}

// AverageRequestLatency returns the average request latency.
func (m *P2PMetrics) AverageRequestLatency() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.requestLatency) == 0 {
		return 0
	}

	var total time.Duration
	for _, l := range m.requestLatency {
		total += l
	}
	return total / time.Duration(len(m.requestLatency))
}

// LogStats logs the current P2P metrics.
func (m *P2PMetrics) LogStats() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	log.Info("P2P metrics",
		"peers_connected", atomic.LoadInt64(&m.peersConnected),
		"peers_disconnected", atomic.LoadInt64(&m.peersDisconnected),
		"peers_banned", atomic.LoadInt64(&m.peersBanned),
		"requests_total", m.requestsTotal,
		"requests_failed", m.requestsFailed,
		"failure_rate", fmt.Sprintf("%.2f%%", m.RequestFailureRate()*100),
		"avg_latency", m.AverageRequestLatency(),
		"blocks_received", atomic.LoadUint64(&m.blocksReceived),
		"bytes_received", atomic.LoadUint64(&m.bytesReceived),
	)
}

// =============================================================================
// Topic Registry (replaces init()-based registration)
// =============================================================================

// TopicRegistry manages gossip topic mappings without using init().
// This provides explicit control over when and how topics are registered.
type TopicRegistry struct {
	mu       sync.RWMutex
	topics   map[string]TopicConfig
	handlers map[string]TopicHandler
}

// TopicConfig holds configuration for a gossip topic.
type TopicConfig struct {
	Name        string
	MessageType string
	Validator   func(data []byte) error
}

// TopicHandler is a function that handles messages for a topic.
type TopicHandler func(ctx context.Context, data []byte, from PeerID) error

// NewTopicRegistry creates a new TopicRegistry.
func NewTopicRegistry() *TopicRegistry {
	return &TopicRegistry{
		topics:   make(map[string]TopicConfig),
		handlers: make(map[string]TopicHandler),
	}
}

// Register registers a topic with its configuration.
func (r *TopicRegistry) Register(config TopicConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.topics[config.Name]; exists {
		return fmt.Errorf("topic %s already registered", config.Name)
	}
	r.topics[config.Name] = config
	return nil
}

// SetHandler sets the handler for a topic.
func (r *TopicRegistry) SetHandler(topic string, handler TopicHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.topics[topic]; !exists {
		return fmt.Errorf("topic %s not registered", topic)
	}
	r.handlers[topic] = handler
	return nil
}

// GetConfig returns the configuration for a topic.
func (r *TopicRegistry) GetConfig(topic string) (TopicConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	config, ok := r.topics[topic]
	return config, ok
}

// GetHandler returns the handler for a topic.
func (r *TopicRegistry) GetHandler(topic string) (TopicHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[topic]
	return handler, ok
}

// AllTopics returns all registered topic names.
func (r *TopicRegistry) AllTopics() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	topics := make([]string, 0, len(r.topics))
	for name := range r.topics {
		topics = append(topics, name)
	}
	return topics
}

// RegisterDefaultTopics registers the default gossip topics.
// Call this explicitly during initialization instead of using init().
func (r *TopicRegistry) RegisterDefaultTopics() error {
	defaults := []TopicConfig{
		{Name: BlockTopicFormat, MessageType: "Block"},
		{Name: TransactionTopicFormat, MessageType: "Transaction"},
	}

	for _, config := range defaults {
		if err := r.Register(config); err != nil {
			return err
		}
	}
	return nil
}

// DefaultTopicRegistry is the global topic registry.
// Use RegisterDefaultTopics() to initialize it explicitly.
var DefaultTopicRegistry = NewTopicRegistry()

// =============================================================================
// Compile-time interface checks
// =============================================================================

// Compile-time checks: Verify SyncP2P embeds all required sub-interfaces.
// These checks ensure that any type implementing SyncP2P also provides
// all the capabilities defined by PeerProvider, BlockRequester,
// TopicSubscriber, and PeerScorer.
var (
	_ PeerProvider    = (SyncP2P)(nil)
	_ BlockRequester  = (SyncP2P)(nil)
	_ TopicSubscriber = (SyncP2P)(nil)
	_ PeerScorer      = (SyncP2P)(nil)
)
