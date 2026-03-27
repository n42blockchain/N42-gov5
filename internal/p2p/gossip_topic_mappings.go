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

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/proto/types_pb"
)

// GossipTopicRegistry manages gossip topic mappings with thread-safe access.
type GossipTopicRegistry struct {
	mu          sync.RWMutex
	topics      map[string]proto.Message
	typeMapping map[reflect.Type]string
}

var (
	globalGossipRegistry = &GossipTopicRegistry{
		topics:      make(map[string]proto.Message),
		typeMapping: make(map[reflect.Type]string),
	}
	initOnce sync.Once
)

// gossipTopicMappings defines the canonical topic-to-message-type mappings.
// Referenced by pubsub_filter.go, service.go, and the registry.
var gossipTopicMappings = map[string]proto.Message{
	BlockTopicFormat:             &types_pb.Block{},
	TransactionTopicFormat:       &types_pb.Transaction{},
	BlobSidecarTopicFormat:       &types_pb.BlobSidecar{},
	HotStuffConsensusTopicFormat: &types_pb.H256{}, // HotStuff uses custom SSZ, not protobuf; H256 is a placeholder for topic registration
	ZKProofTopicFormat:           &types_pb.H256{}, // ZK proofs use custom serialization; H256 placeholder for topic registration
}

// GossipTypeMapping is the inverse mapping (message type -> topic) used by broadcaster.go.
var GossipTypeMapping = make(map[reflect.Type]string, len(gossipTopicMappings))

func init() {
	for k, v := range gossipTopicMappings {
		GossipTypeMapping[reflect.TypeOf(v)] = k
	}
	InitGossipTopics()
}

// InitGossipTopics initializes the gossip topic registry with default topics.
// Safe to call multiple times; subsequent calls are no-ops.
func InitGossipTopics() {
	initOnce.Do(func() {
		globalGossipRegistry.mu.Lock()
		defer globalGossipRegistry.mu.Unlock()

		for topic, msg := range gossipTopicMappings {
			globalGossipRegistry.topics[topic] = msg
			globalGossipRegistry.typeMapping[reflect.TypeOf(msg)] = topic
		}
	})
}

// RegisterGossipTopic registers a custom gossip topic mapping.
func RegisterGossipTopic(topic string, msg proto.Message) {
	globalGossipRegistry.mu.Lock()
	defer globalGossipRegistry.mu.Unlock()

	globalGossipRegistry.topics[topic] = msg
	globalGossipRegistry.typeMapping[reflect.TypeOf(msg)] = topic
}

// GossipTopicMappings returns the protobuf message type for a gossip topic.
func GossipTopicMappings(topic string) proto.Message {
	InitGossipTopics()

	globalGossipRegistry.mu.RLock()
	defer globalGossipRegistry.mu.RUnlock()

	return globalGossipRegistry.topics[topic]
}

// AllTopics returns all registered gossip topic names.
func AllTopics() []string {
	InitGossipTopics()

	globalGossipRegistry.mu.RLock()
	defer globalGossipRegistry.mu.RUnlock()

	topics := make([]string, 0, len(globalGossipRegistry.topics))
	for k := range globalGossipRegistry.topics {
		topics = append(topics, k)
	}
	return topics
}

// GossipTypeToTopic returns the gossip topic for a given protobuf message type.
func GossipTypeToTopic(msg proto.Message) string {
	InitGossipTopics()

	globalGossipRegistry.mu.RLock()
	defer globalGossipRegistry.mu.RUnlock()

	return globalGossipRegistry.typeMapping[reflect.TypeOf(msg)]
}

// IsGossipTopicsInitialized reports whether the registry has been initialized.
func IsGossipTopicsInitialized() bool {
	globalGossipRegistry.mu.RLock()
	defer globalGossipRegistry.mu.RUnlock()
	return len(globalGossipRegistry.topics) > 0
}

// ResetGossipTopics resets the registry state. Intended for testing only.
func ResetGossipTopics() {
	initOnce = sync.Once{}

	globalGossipRegistry.mu.Lock()
	defer globalGossipRegistry.mu.Unlock()

	globalGossipRegistry.topics = make(map[string]proto.Message)
	globalGossipRegistry.typeMapping = make(map[reflect.Type]string)
}
