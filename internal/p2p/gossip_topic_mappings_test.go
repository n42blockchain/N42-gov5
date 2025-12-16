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

	"github.com/n42blockchain/N42/api/protocol/types_pb"
)

func TestInitGossipTopics(t *testing.T) {
	// Reset first
	ResetGossipTopics()

	if IsGossipTopicsInitialized() {
		t.Error("Should not be initialized after reset")
	}

	InitGossipTopics()

	if !IsGossipTopicsInitialized() {
		t.Error("Should be initialized after InitGossipTopics()")
	}

	// Second call should be a no-op
	InitGossipTopics()
	if !IsGossipTopicsInitialized() {
		t.Error("Should still be initialized")
	}
}

func TestGossipTopicMappings(t *testing.T) {
	// Ensure initialized
	InitGossipTopics()

	// Test block topic
	blockMsg := GossipTopicMappings(BlockTopicFormat)
	if blockMsg == nil {
		t.Error("Block topic mapping should not be nil")
	}
	if _, ok := blockMsg.(*types_pb.Block); !ok {
		t.Error("Block topic should map to types_pb.Block")
	}

	// Test transaction topic
	txMsg := GossipTopicMappings(TransactionTopicFormat)
	if txMsg == nil {
		t.Error("Transaction topic mapping should not be nil")
	}
	if _, ok := txMsg.(*types_pb.Transaction); !ok {
		t.Error("Transaction topic should map to types_pb.Transaction")
	}

	// Test non-existent topic
	nilMsg := GossipTopicMappings("non-existent")
	if nilMsg != nil {
		t.Error("Non-existent topic should return nil")
	}
}

func TestAllTopics(t *testing.T) {
	InitGossipTopics()

	topics := AllTopics()
	if len(topics) < 2 {
		t.Errorf("AllTopics() should return at least 2 topics, got %d", len(topics))
	}

	// Check that required topics are present
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
		t.Error("Block topic not found in AllTopics()")
	}
	if !hasTx {
		t.Error("Transaction topic not found in AllTopics()")
	}
}

func TestGossipTypeToTopic(t *testing.T) {
	InitGossipTopics()

	// Test block type
	blockTopic := GossipTypeToTopic(&types_pb.Block{})
	if blockTopic != BlockTopicFormat {
		t.Errorf("Block type should map to %s, got %s", BlockTopicFormat, blockTopic)
	}

	// Test transaction type
	txTopic := GossipTypeToTopic(&types_pb.Transaction{})
	if txTopic != TransactionTopicFormat {
		t.Errorf("Transaction type should map to %s, got %s", TransactionTopicFormat, txTopic)
	}
}

func TestRegisterGossipTopic(t *testing.T) {
	ResetGossipTopics()
	InitGossipTopics()

	// Register a custom topic
	customTopic := "/n42/custom/1"
	customMsg := &types_pb.Block{} // Using existing type for simplicity

	RegisterGossipTopic(customTopic, customMsg)

	// Verify registration
	retrieved := GossipTopicMappings(customTopic)
	if retrieved == nil {
		t.Error("Custom topic should be registered")
	}

	// Verify in AllTopics
	found := false
	for _, topic := range AllTopics() {
		if topic == customTopic {
			found = true
			break
		}
	}
	if !found {
		t.Error("Custom topic should appear in AllTopics()")
	}
}

func TestGossipTopicsConcurrency(t *testing.T) {
	ResetGossipTopics()

	var wg sync.WaitGroup

	// Concurrent initialization and access
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
			_ = GossipTopicMappings(BlockTopicFormat)
			_ = GossipTypeToTopic(&types_pb.Block{})
			_ = IsGossipTopicsInitialized()
		}(i)
	}

	wg.Wait()
	t.Log("✓ Gossip topics concurrent operations completed without race")
}

func TestAutoInitialization(t *testing.T) {
	ResetGossipTopics()

	// Without explicit init, should auto-initialize on first access
	msg := GossipTopicMappings(BlockTopicFormat)
	if msg == nil {
		t.Error("Auto-initialization should work for GossipTopicMappings")
	}

	ResetGossipTopics()

	// Same for AllTopics
	topics := AllTopics()
	if len(topics) == 0 {
		t.Error("Auto-initialization should work for AllTopics")
	}

	ResetGossipTopics()

	// Same for GossipTypeToTopic
	topic := GossipTypeToTopic(&types_pb.Block{})
	if topic == "" {
		t.Error("Auto-initialization should work for GossipTypeToTopic")
	}
}

func TestLegacyCompatibility(t *testing.T) {
	// Test that legacy global maps are still populated
	if len(gossipTopicMappings) < 2 {
		t.Error("Legacy gossipTopicMappings should be populated")
	}
	if len(GossipTypeMapping) < 2 {
		t.Error("Legacy GossipTypeMapping should be populated")
	}

	// Check specific mappings
	if _, ok := gossipTopicMappings[BlockTopicFormat]; !ok {
		t.Error("Legacy block topic mapping missing")
	}
	if _, ok := gossipTopicMappings[TransactionTopicFormat]; !ok {
		t.Error("Legacy transaction topic mapping missing")
	}
}
