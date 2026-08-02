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
	"sync"
	"testing"
)

func TestInitGossipTopics(t *testing.T) {
	ResetGossipTopics()

	if IsGossipTopicsInitialized() {
		t.Error("should not be initialized after reset")
	}

	InitGossipTopics()

	if !IsGossipTopicsInitialized() {
		t.Error("should be initialized after InitGossipTopics()")
	}

	// Second call should be a no-op.
	InitGossipTopics()
	if !IsGossipTopicsInitialized() {
		t.Error("should still be initialized")
	}
}

// TestIsGossipTopic checks membership, which is what the registry is for:
// subscribe() refuses a topic that is not registered, because nothing
// downstream would know how to decode it.
func TestIsGossipTopic(t *testing.T) {
	InitGossipTopics()

	for _, topic := range []string{
		BlockTopicFormat,
		TransactionTopicFormat,
		BlobSidecarTopicFormat,
		HotStuffConsensusTopicFormat,
		H2V4Topic,
	} {
		if !IsGossipTopic(topic) {
			t.Errorf("%s must stay registered or subscribe() will refuse it", topic)
		}
	}

	if IsGossipTopic("non-existent") {
		t.Error("an unregistered topic should not report as registered")
	}
}

// TestEveryRegisteredTopicHasADecoder guards the gap that used to be papered
// over by a protobuf fallback: a topic could be registered, and therefore
// subscribable, while decodePubsubMessage had no case for it. The fallback
// decoded such a topic into a placeholder H256 and passed it on. Any topic
// this node subscribes to must be decodable, so the two lists must agree.
func TestEveryRegisteredTopicHasADecoder(t *testing.T) {
	InitGossipTopics()

	// Topics decodePubsubMessage handles. Topics served by their own
	// subscription paths (HotStuff, ZK, mobile) never reach it.
	decodable := map[string]bool{
		BlockTopicFormat:       true,
		TransactionTopicFormat: true,
		BlobSidecarTopicFormat: true,
	}
	selfDecoding := map[string]bool{
		HotStuffConsensusTopicFormat:  true,
		H2V4Topic:                     true,
		ZKProofTopicFormat:            true,
		MobilePacketTopicFormat:       true,
		MobileRegistrationTopicFormat: true,
		MobileCohortIndexTopicFormat:  true,
		MobileCohortRevealTopicFormat: true,
		MobileCohortCertTopicFormat:   true,
	}

	for _, topic := range AllTopics() {
		if !decodable[topic] && !selfDecoding[topic] {
			t.Errorf("topic %s is registered but nothing decodes it", topic)
		}
	}
}

func TestAllTopics(t *testing.T) {
	InitGossipTopics()

	topics := AllTopics()
	if len(topics) < 2 {
		t.Errorf("AllTopics() should return at least 2 topics, got %d", len(topics))
	}

	hasBlock := false
	hasTx := false
	for _, topic := range topics {
		if topic == BlockTopicFormat {
			hasBlock = true
		}
		if topic == TransactionTopicFormat {
			hasTx = true
		}
	}

	if !hasBlock {
		t.Error("block topic not found in AllTopics()")
	}
	if !hasTx {
		t.Error("transaction topic not found in AllTopics()")
	}
}

func TestRegisterGossipTopic(t *testing.T) {
	ResetGossipTopics()
	InitGossipTopics()

	customTopic := "/n42/custom/1"
	RegisterGossipTopic(customTopic)

	if !IsGossipTopic(customTopic) {
		t.Error("custom topic should be registered")
	}

	found := false
	for _, topic := range AllTopics() {
		if topic == customTopic {
			found = true
			break
		}
	}
	if !found {
		t.Error("custom topic should appear in AllTopics()")
	}
}

func TestGossipTopicsConcurrency(t *testing.T) {
	ResetGossipTopics()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				InitGossipTopics()
			}
			if i%5 == 0 {
				_ = AllTopics()
			}
			_ = IsGossipTopic(BlockTopicFormat)
			_ = IsGossipTopicsInitialized()
		}(i)
	}
	wg.Wait()
}

func TestAutoInitialization(t *testing.T) {
	ResetGossipTopics()
	if !IsGossipTopic(BlockTopicFormat) {
		t.Error("auto-initialization should work for IsGossipTopic")
	}

	ResetGossipTopics()
	topics := AllTopics()
	if len(topics) == 0 {
		t.Error("auto-initialization should work for AllTopics")
	}
}
