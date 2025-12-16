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
	"sync"
	"testing"
	"time"
)

// =============================================================================
// P2PMetrics Tests
// =============================================================================

func TestNewP2PMetrics(t *testing.T) {
	m := NewP2PMetrics()
	if m == nil {
		t.Fatal("NewP2PMetrics() returned nil")
	}
	if m.requestLatency == nil {
		t.Error("requestLatency not initialized")
	}
}

func TestP2PMetricsPeerEvents(t *testing.T) {
	m := NewP2PMetrics()

	m.RecordPeerConnect()
	m.RecordPeerConnect()
	m.RecordPeerDisconnect()
	m.RecordPeerBan()

	if m.peersConnected != 2 {
		t.Errorf("peersConnected = %d, want 2", m.peersConnected)
	}
	if m.peersDisconnected != 1 {
		t.Errorf("peersDisconnected = %d, want 1", m.peersDisconnected)
	}
	if m.peersBanned != 1 {
		t.Errorf("peersBanned = %d, want 1", m.peersBanned)
	}
}

func TestP2PMetricsRequestFailureRate(t *testing.T) {
	m := NewP2PMetrics()

	// No requests yet
	if rate := m.RequestFailureRate(); rate != 0 {
		t.Errorf("RequestFailureRate() = %v, want 0", rate)
	}

	// 25% failure rate
	for i := 0; i < 75; i++ {
		m.RecordRequest(100*time.Millisecond, true)
	}
	for i := 0; i < 25; i++ {
		m.RecordRequest(100*time.Millisecond, false)
	}

	rate := m.RequestFailureRate()
	if rate < 0.24 || rate > 0.26 {
		t.Errorf("RequestFailureRate() = %v, want ~0.25", rate)
	}
}

func TestP2PMetricsAverageRequestLatency(t *testing.T) {
	m := NewP2PMetrics()

	// No latencies yet
	if latency := m.AverageRequestLatency(); latency != 0 {
		t.Errorf("AverageRequestLatency() = %v, want 0", latency)
	}

	// Add latencies
	m.RecordRequest(100*time.Millisecond, true)
	m.RecordRequest(200*time.Millisecond, true)
	m.RecordRequest(300*time.Millisecond, true)

	avgLatency := m.AverageRequestLatency()
	expected := 200 * time.Millisecond
	if avgLatency < 190*time.Millisecond || avgLatency > 210*time.Millisecond {
		t.Errorf("AverageRequestLatency() = %v, want ~%v", avgLatency, expected)
	}
}

func TestP2PMetricsBlocksReceived(t *testing.T) {
	m := NewP2PMetrics()

	m.RecordBlocksReceived(10, 1000)
	m.RecordBlocksReceived(20, 2000)

	if m.blocksReceived != 30 {
		t.Errorf("blocksReceived = %d, want 30", m.blocksReceived)
	}
	if m.bytesReceived != 3000 {
		t.Errorf("bytesReceived = %d, want 3000", m.bytesReceived)
	}
}

func TestP2PMetricsConcurrency(t *testing.T) {
	m := NewP2PMetrics()
	var wg sync.WaitGroup

	// Concurrent operations
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.RecordPeerConnect()
			m.RecordPeerDisconnect()
			m.RecordRequest(100*time.Millisecond, true)
			m.RecordBlocksReceived(1, 100)
			_ = m.RequestFailureRate()
			_ = m.AverageRequestLatency()
		}()
	}

	wg.Wait()
	t.Log("✓ P2PMetrics concurrent operations completed without race")
}

// =============================================================================
// TopicRegistry Tests
// =============================================================================

func TestNewTopicRegistry(t *testing.T) {
	r := NewTopicRegistry()
	if r == nil {
		t.Fatal("NewTopicRegistry() returned nil")
	}
	if r.topics == nil {
		t.Error("topics not initialized")
	}
	if r.handlers == nil {
		t.Error("handlers not initialized")
	}
}

func TestTopicRegistryRegister(t *testing.T) {
	r := NewTopicRegistry()

	config := TopicConfig{
		Name:        "test-topic",
		MessageType: "TestMessage",
	}

	// First registration should succeed
	if err := r.Register(config); err != nil {
		t.Errorf("Register() error = %v", err)
	}

	// Duplicate registration should fail
	if err := r.Register(config); err == nil {
		t.Error("Duplicate Register() should return error")
	}
}

func TestTopicRegistryGetConfig(t *testing.T) {
	r := NewTopicRegistry()

	config := TopicConfig{
		Name:        "test-topic",
		MessageType: "TestMessage",
	}
	_ = r.Register(config)

	// Get existing config
	retrieved, ok := r.GetConfig("test-topic")
	if !ok {
		t.Error("GetConfig() returned false for existing topic")
	}
	if retrieved.Name != config.Name {
		t.Errorf("GetConfig().Name = %s, want %s", retrieved.Name, config.Name)
	}

	// Get non-existing config
	_, ok = r.GetConfig("non-existing")
	if ok {
		t.Error("GetConfig() returned true for non-existing topic")
	}
}

func TestTopicRegistrySetHandler(t *testing.T) {
	r := NewTopicRegistry()

	config := TopicConfig{
		Name:        "test-topic",
		MessageType: "TestMessage",
	}
	_ = r.Register(config)

	handler := func(ctx context.Context, data []byte, from PeerID) error {
		return nil
	}

	// Set handler for existing topic
	if err := r.SetHandler("test-topic", handler); err != nil {
		t.Errorf("SetHandler() error = %v", err)
	}

	// Set handler for non-existing topic should fail
	if err := r.SetHandler("non-existing", handler); err == nil {
		t.Error("SetHandler() for non-existing topic should return error")
	}
}

func TestTopicRegistryGetHandler(t *testing.T) {
	r := NewTopicRegistry()

	config := TopicConfig{
		Name:        "test-topic",
		MessageType: "TestMessage",
	}
	_ = r.Register(config)

	handler := func(ctx context.Context, data []byte, from PeerID) error {
		return nil
	}
	_ = r.SetHandler("test-topic", handler)

	// Get existing handler
	_, ok := r.GetHandler("test-topic")
	if !ok {
		t.Error("GetHandler() returned false for existing handler")
	}

	// Get non-existing handler
	_, ok = r.GetHandler("non-existing")
	if ok {
		t.Error("GetHandler() returned true for non-existing handler")
	}
}

func TestTopicRegistryAllTopics(t *testing.T) {
	r := NewTopicRegistry()

	_ = r.Register(TopicConfig{Name: "topic1", MessageType: "Type1"})
	_ = r.Register(TopicConfig{Name: "topic2", MessageType: "Type2"})
	_ = r.Register(TopicConfig{Name: "topic3", MessageType: "Type3"})

	topics := r.AllTopics()
	if len(topics) != 3 {
		t.Errorf("AllTopics() len = %d, want 3", len(topics))
	}
}

func TestTopicRegistryRegisterDefaultTopics(t *testing.T) {
	r := NewTopicRegistry()

	if err := r.RegisterDefaultTopics(); err != nil {
		t.Errorf("RegisterDefaultTopics() error = %v", err)
	}

	topics := r.AllTopics()
	if len(topics) < 2 {
		t.Errorf("RegisterDefaultTopics() should register at least 2 topics, got %d", len(topics))
	}
}

func TestTopicRegistryConcurrency(t *testing.T) {
	r := NewTopicRegistry()
	var wg sync.WaitGroup

	// Register some topics first
	for i := 0; i < 10; i++ {
		_ = r.Register(TopicConfig{
			Name:        string(rune('a' + i)),
			MessageType: "Type",
		})
	}

	// Concurrent operations
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			topic := string(rune('a' + i%10))
			_, _ = r.GetConfig(topic)
			_, _ = r.GetHandler(topic)
			_ = r.AllTopics()
		}(i)
	}

	wg.Wait()
	t.Log("✓ TopicRegistry concurrent operations completed without race")
}

// =============================================================================
// DefaultTopicRegistry Tests
// =============================================================================

func TestDefaultTopicRegistry(t *testing.T) {
	if DefaultTopicRegistry == nil {
		t.Error("DefaultTopicRegistry is nil")
	}
}

// =============================================================================
// Interface Definition Tests
// =============================================================================

func TestSyncP2PInterfaceExists(t *testing.T) {
	// This test verifies that the interface is properly defined
	var _ SyncP2P = (SyncP2P)(nil)
	t.Log("✓ SyncP2P interface is defined")
}

func TestPeerProviderInterfaceExists(t *testing.T) {
	var _ PeerProvider = (PeerProvider)(nil)
	t.Log("✓ PeerProvider interface is defined")
}

func TestBlockRequesterInterfaceExists(t *testing.T) {
	var _ BlockRequester = (BlockRequester)(nil)
	t.Log("✓ BlockRequester interface is defined")
}

func TestTopicSubscriberInterfaceExists(t *testing.T) {
	var _ TopicSubscriber = (TopicSubscriber)(nil)
	t.Log("✓ TopicSubscriber interface is defined")
}

func TestPeerScorerInterfaceExists(t *testing.T) {
	var _ PeerScorer = (PeerScorer)(nil)
	t.Log("✓ PeerScorer interface is defined")
}

