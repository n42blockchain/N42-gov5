// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestBlocksForAddresses covers the multi-address OR + range-trim path.
func TestBlocksForAddresses(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addrA := types.HexToAddress("0x3333333333333333333333333333333333333333")
	addrB := types.HexToAddress("0x4444444444444444444444444444444444444444")

	if err := WriteLogIndex(tx, 10, makeReceipts([]*block.Log{makeLog(addrA)})); err != nil {
		t.Fatalf("WriteLogIndex(10): %v", err)
	}
	if err := WriteLogIndex(tx, 20, makeReceipts([]*block.Log{makeLog(addrB)})); err != nil {
		t.Fatalf("WriteLogIndex(20): %v", err)
	}
	if err := WriteLogIndex(tx, 30, makeReceipts([]*block.Log{makeLog(addrA)})); err != nil {
		t.Fatalf("WriteLogIndex(30): %v", err)
	}

	bm, err := BlocksForAddresses(tx, []types.Address{addrA, addrB}, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForAddresses: %v", err)
	}
	if bm.GetCardinality() != 3 || !bm.ContainsInt(10) || !bm.ContainsInt(20) || !bm.ContainsInt(30) {
		t.Fatalf("unexpected bitmap: %v", bm.ToArray())
	}

	// Range trim excludes block 30.
	trimmed, err := BlocksForAddresses(tx, []types.Address{addrA, addrB}, 0, 25)
	if err != nil {
		t.Fatalf("BlocksForAddresses(trimmed): %v", err)
	}
	if trimmed.ContainsInt(30) {
		t.Fatal("block 30 should have been trimmed out of range")
	}
	if !trimmed.ContainsInt(10) || !trimmed.ContainsInt(20) {
		t.Fatal("blocks within range were incorrectly trimmed")
	}

	// Out-of-uint32 range is rejected.
	if _, err := BlocksForAddresses(tx, []types.Address{addrA}, 0, uint64(1)<<33); err == nil {
		t.Fatal("expected error for out-of-range block number")
	}
}

// TestBlocksForTopics covers the wildcard-skip, OR-within-position, and
// AND-across-positions semantics, plus the all-wildcard nil result.
func TestBlocksForTopics(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addr := types.HexToAddress("0x5555555555555555555555555555555555555555")
	topicX := types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	topicY := types.HexToHash("0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	topicZ := types.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	// Block 1: topic0=X, topic1=Y
	if err := WriteLogIndex(tx, 1, makeReceipts([]*block.Log{makeLog(addr, topicX, topicY)})); err != nil {
		t.Fatalf("WriteLogIndex(1): %v", err)
	}
	// Block 2: topic0=X, topic1=Z
	if err := WriteLogIndex(tx, 2, makeReceipts([]*block.Log{makeLog(addr, topicX, topicZ)})); err != nil {
		t.Fatalf("WriteLogIndex(2): %v", err)
	}
	// Block 3: topic0=Y only (no match for topic0 filter below)
	if err := WriteLogIndex(tx, 3, makeReceipts([]*block.Log{makeLog(addr, topicY)})); err != nil {
		t.Fatalf("WriteLogIndex(3): %v", err)
	}

	// Filter: position0 in {X}, position1 wildcard -> blocks 1, 2.
	bm, err := BlocksForTopics(tx, [][]types.Hash{{topicX}, {}}, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForTopics(wildcard pos1): %v", err)
	}
	if bm == nil || bm.GetCardinality() != 2 || !bm.ContainsInt(1) || !bm.ContainsInt(2) {
		t.Fatalf("unexpected bitmap: %v", bm)
	}

	// Filter: position0 in {X}, position1 in {Y} -> AND -> block 1 only.
	bm, err = BlocksForTopics(tx, [][]types.Hash{{topicX}, {topicY}}, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForTopics(AND): %v", err)
	}
	if bm.GetCardinality() != 1 || !bm.ContainsInt(1) {
		t.Fatalf("unexpected AND result: %v", bm.ToArray())
	}

	// Filter: position1 in {Y, Z} (OR within position). The topic index is
	// not position-aware (it maps topic -> blocks containing it anywhere in
	// the log), so this also picks up block 3's position-0 topicY.
	bm, err = BlocksForTopics(tx, [][]types.Hash{{}, {topicY, topicZ}}, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForTopics(OR): %v", err)
	}
	if bm.GetCardinality() != 3 || !bm.ContainsInt(1) || !bm.ContainsInt(2) || !bm.ContainsInt(3) {
		t.Fatalf("unexpected OR result: %v", bm.ToArray())
	}

	// All wildcard positions -> nil, nil.
	bm, err = BlocksForTopics(tx, [][]types.Hash{{}, {}}, 0, 100)
	if err != nil || bm != nil {
		t.Fatalf("BlocksForTopics(all wildcard) = %v, %v, want nil, nil", bm, err)
	}

	// No positions at all -> nil, nil.
	bm, err = BlocksForTopics(tx, nil, 0, 100)
	if err != nil || bm != nil {
		t.Fatalf("BlocksForTopics(nil) = %v, %v, want nil, nil", bm, err)
	}

	// Out-of-uint32 range is rejected.
	if _, err := BlocksForTopics(tx, [][]types.Hash{{topicX}}, 0, uint64(1)<<33); err == nil {
		t.Fatal("expected error for out-of-range block number")
	}
}
