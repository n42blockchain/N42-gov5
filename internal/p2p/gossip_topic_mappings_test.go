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

	"github.com/n42blockchain/N42/proto/types_pb"
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

func TestGossipTopicMappings(t *testing.T) {
	InitGossipTopics()

	blockMsg := GossipTopicMappings(BlockTopicFormat)
	if blockMsg == nil {
		t.Fatal("block topic mapping should not be nil")
	}
	if _, ok := blockMsg.(*types_pb.Block); !ok {
		t.Error("block topic should map to types_pb.Block")
	}

	txMsg := GossipTopicMappings(TransactionTopicFormat)
	if txMsg == nil {
		t.Fatal("transaction topic mapping should not be nil")
	}
	if _, ok := txMsg.(*types_pb.Transaction); !ok {
		t.Error("transaction topic should map to types_pb.Transaction")
	}

	nilMsg := GossipTopicMappings("non-existent")
	if nilMsg != nil {
		t.Error("non-existent topic should return nil")
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

func TestGossipTypeToTopic(t *testing.T) {
	InitGossipTopics()

	blockTopic := GossipTypeToTopic(&types_pb.Block{})
	if blockTopic != BlockTopicFormat {
		t.Errorf("block type should map to %s, got %s", BlockTopicFormat, blockTopic)
	}

	txTopic := GossipTypeToTopic(&types_pb.Transaction{})
	if txTopic != TransactionTopicFormat {
		t.Errorf("transaction type should map to %s, got %s", TransactionTopicFormat, txTopic)
	}
}

func TestRegisterGossipTopic(t *testing.T) {
	ResetGossipTopics()
	InitGossipTopics()

	customTopic := "/n42/custom/1"
	RegisterGossipTopic(customTopic, &types_pb.Block{})

	retrieved := GossipTopicMappings(customTopic)
	if retrieved == nil {
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
			_ = GossipTopicMappings(BlockTopicFormat)
			_ = GossipTypeToTopic(&types_pb.Block{})
			_ = IsGossipTopicsInitialized()
		}(i)
	}
	wg.Wait()
}

func TestAutoInitialization(t *testing.T) {
	ResetGossipTopics()
	msg := GossipTopicMappings(BlockTopicFormat)
	if msg == nil {
		t.Error("auto-initialization should work for GossipTopicMappings")
	}

	ResetGossipTopics()
	topics := AllTopics()
	if len(topics) == 0 {
		t.Error("auto-initialization should work for AllTopics")
	}

	ResetGossipTopics()
	topic := GossipTypeToTopic(&types_pb.Block{})
	if topic == "" {
		t.Error("auto-initialization should work for GossipTypeToTopic")
	}
}

func TestLegacyCompatibility(t *testing.T) {
	if len(gossipTopicMappings) < 2 {
		t.Error("legacy gossipTopicMappings should be populated")
	}
	if len(GossipTypeMapping) < 2 {
		t.Error("legacy GossipTypeMapping should be populated")
	}

	if _, ok := gossipTopicMappings[BlockTopicFormat]; !ok {
		t.Error("legacy block topic mapping missing")
	}
	if _, ok := gossipTopicMappings[TransactionTopicFormat]; !ok {
		t.Error("legacy transaction topic mapping missing")
	}
}
