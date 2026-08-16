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
//
// GossipSub topic registry: the set of topics this node recognises.
//
// This used to map each topic to a generated protobuf message, and the
// subscriber decoded by cloning that message and unmarshalling into it. Topic
// by topic the payloads moved to their own encodings -- blocks and transactions
// to RLP, blob sidecars to rawdb's storage encoding, HotStuff, ZK proofs and
// the mobile topics to compact formats of their own -- and each migration left
// behind a placeholder entry pointing at types_pb.H256 so the topic would still
// register. By the end every entry pointed at the same placeholder, which made
// the reverse "message type -> topic" index degenerate: ten topics, one type,
// last write wins. A registry that types nothing is worse than no registry,
// because it still looks authoritative. What callers actually need is
// membership, so that is all this holds now.

package p2p

import "sync"

// GossipTopicRegistry holds the recognised gossip topics with thread-safe access.
type GossipTopicRegistry struct {
	mu     sync.RWMutex
	topics map[string]struct{}
}

var (
	globalGossipRegistry = &GossipTopicRegistry{topics: make(map[string]struct{})}
	initOnce             sync.Once
)

// gossipTopics is the canonical set of topics this node recognises. A topic
// missing from it cannot be subscribed to: subscribe() treats that as a
// configuration error rather than guessing.
var gossipTopics = []string{
	BlockTopicFormat,              // RLP block
	TransactionTopicFormat,        // RLP transaction
	BlobSidecarTopicFormat,        // rawdb blob sidecar encoding
	HotStuffConsensusTopicFormat,  // HotStuff compact SSZ
	H2V4Topic,                     // chain-bound H2-v4 Decide proofs (compact wire format)
	ZKProofTopicFormat,            // ZK proof serialization
	MobilePacketTopicFormat,       // evmsdk V2 stream packets
	MobileRegistrationTopicFormat, // raw pubkey||pop
	MobileCohortIndexTopicFormat,  // compact cohort index announcements
	MobileCohortRevealTopicFormat, // compact cohort reveal announcements
	MobileCohortCertTopicFormat,   // compact cohort cert announcements
}

func init() {
	InitGossipTopics()
}

// InitGossipTopics initializes the gossip topic registry with the default
// topics. Safe to call multiple times; subsequent calls are no-ops.
func InitGossipTopics() {
	initOnce.Do(func() {
		globalGossipRegistry.mu.Lock()
		defer globalGossipRegistry.mu.Unlock()

		for _, topic := range gossipTopics {
			globalGossipRegistry.topics[topic] = struct{}{}
		}
	})
}

// RegisterGossipTopic registers an additional gossip topic.
func RegisterGossipTopic(topic string) {
	globalGossipRegistry.mu.Lock()
	defer globalGossipRegistry.mu.Unlock()

	globalGossipRegistry.topics[topic] = struct{}{}
}

// IsGossipTopic reports whether topic is one this node recognises.
func IsGossipTopic(topic string) bool {
	InitGossipTopics()

	globalGossipRegistry.mu.RLock()
	defer globalGossipRegistry.mu.RUnlock()

	_, ok := globalGossipRegistry.topics[topic]
	return ok
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

	globalGossipRegistry.topics = make(map[string]struct{})
}
