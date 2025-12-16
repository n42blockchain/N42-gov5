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
	"reflect"
	"sync"

	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"google.golang.org/protobuf/proto"
)

// =============================================================================
// Gossip Topic Registry (explicit registration, no init())
// =============================================================================

// GossipTopicRegistry manages gossip topic mappings without using init().
// This provides explicit control over when topics are registered.
type GossipTopicRegistry struct {
	mu          sync.RWMutex
	topics      map[string]proto.Message
	typeMapping map[reflect.Type]string
	initialized bool
}

// globalGossipRegistry is the singleton registry instance.
// Call InitGossipTopics() to initialize it.
var globalGossipRegistry = &GossipTopicRegistry{
	topics:      make(map[string]proto.Message),
	typeMapping: make(map[reflect.Type]string),
}

// initOnce ensures InitGossipTopics is called only once even in concurrent scenarios.
var initOnce sync.Once

// InitGossipTopics initializes the gossip topic registry with default topics.
// This replaces the old init() function and should be called explicitly
// during node startup.
//
// Safe to call multiple times - subsequent calls are no-ops.
// Thread-safe via sync.Once.
func InitGossipTopics() {
	initOnce.Do(func() {
		globalGossipRegistry.mu.Lock()
		defer globalGossipRegistry.mu.Unlock()

		// Register default topics
		defaultTopics := map[string]proto.Message{
			BlockTopicFormat:       &types_pb.Block{},
			TransactionTopicFormat: &types_pb.Transaction{},
		}

		for topic, msg := range defaultTopics {
			globalGossipRegistry.topics[topic] = msg
			globalGossipRegistry.typeMapping[reflect.TypeOf(msg)] = topic
		}

		globalGossipRegistry.initialized = true
	})
}

// RegisterGossipTopic registers a custom gossip topic.
// This allows extensions to add new topics without modifying this file.
func RegisterGossipTopic(topic string, msg proto.Message) {
	globalGossipRegistry.mu.Lock()
	defer globalGossipRegistry.mu.Unlock()

	globalGossipRegistry.topics[topic] = msg
	globalGossipRegistry.typeMapping[reflect.TypeOf(msg)] = topic
}

// GossipTopicMappings returns the message type for a topic.
func GossipTopicMappings(topic string) proto.Message {
	// Ensure initialized (safe via sync.Once)
	InitGossipTopics()

	globalGossipRegistry.mu.RLock()
	defer globalGossipRegistry.mu.RUnlock()

	return globalGossipRegistry.topics[topic]
}

// AllTopics returns all registered topic names.
func AllTopics() []string {
	// Ensure initialized (safe via sync.Once)
	InitGossipTopics()

	globalGossipRegistry.mu.RLock()
	defer globalGossipRegistry.mu.RUnlock()

	topics := make([]string, 0, len(globalGossipRegistry.topics))
	for k := range globalGossipRegistry.topics {
		topics = append(topics, k)
	}
	return topics
}

// GossipTypeToTopic returns the topic for a message type.
func GossipTypeToTopic(msg proto.Message) string {
	// Ensure initialized (safe via sync.Once)
	InitGossipTopics()

	globalGossipRegistry.mu.RLock()
	defer globalGossipRegistry.mu.RUnlock()

	return globalGossipRegistry.typeMapping[reflect.TypeOf(msg)]
}

// IsInitialized returns whether the registry has been initialized.
func IsGossipTopicsInitialized() bool {
	globalGossipRegistry.mu.RLock()
	defer globalGossipRegistry.mu.RUnlock()
	return globalGossipRegistry.initialized
}

// ResetGossipTopics resets the registry (for testing only).
// WARNING: This function is NOT thread-safe with concurrent access.
// Only use in test setup/teardown when no other goroutines are accessing the registry.
func ResetGossipTopics() {
	globalGossipRegistry.mu.Lock()
	defer globalGossipRegistry.mu.Unlock()

	globalGossipRegistry.topics = make(map[string]proto.Message)
	globalGossipRegistry.typeMapping = make(map[reflect.Type]string)
	globalGossipRegistry.initialized = false

	// Reset sync.Once by creating a new instance
	// This is safe only in test scenarios
	initOnce = sync.Once{}
}

// =============================================================================
// Legacy compatibility (deprecated, use the above functions)
// =============================================================================

// gossipTopicMappings is kept for backward compatibility.
// Deprecated: Use GossipTopicMappings() function instead.
var gossipTopicMappings = map[string]proto.Message{
	BlockTopicFormat:       &types_pb.Block{},
	TransactionTopicFormat: &types_pb.Transaction{},
}

// GossipTypeMapping is the inverse mapping.
// Deprecated: Use GossipTypeToTopic() function instead.
var GossipTypeMapping = make(map[reflect.Type]string, len(gossipTopicMappings))

// init is kept for backward compatibility but only initializes the legacy maps.
// The new registry should be initialized explicitly via InitGossipTopics().
func init() {
	// Initialize legacy maps for backward compatibility
	for k, v := range gossipTopicMappings {
		GossipTypeMapping[reflect.TypeOf(v)] = k
	}
	// Also initialize the new registry
	InitGossipTopics()
}
