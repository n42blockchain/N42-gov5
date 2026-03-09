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

package rawdb

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func init() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

// makeReceipts creates a receipt slice with the given logs.
func makeReceipts(logs ...[]*block.Log) block.Receipts {
	receipts := make(block.Receipts, len(logs))
	for i, l := range logs {
		receipts[i] = &block.Receipt{Logs: l}
	}
	return receipts
}

func makeLog(addr types.Address, topics ...types.Hash) *block.Log {
	return &block.Log{
		Address: addr,
		Topics:  topics,
	}
}

func TestWriteLogIndex_SingleBlock(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addr := types.HexToAddress("0x1111111111111111111111111111111111111111")
	topic := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	receipts := makeReceipts([]*block.Log{makeLog(addr, topic)})

	if err := WriteLogIndex(tx, 100, receipts); err != nil {
		t.Fatalf("WriteLogIndex failed: %v", err)
	}

	// Verify address index.
	blocks, err := BlocksForAddress(tx, addr, 0, 200)
	if err != nil {
		t.Fatalf("BlocksForAddress failed: %v", err)
	}
	if len(blocks) != 1 || blocks[0] != 100 {
		t.Fatalf("expected [100], got %v", blocks)
	}

	// Verify topic index.
	blocks, err = BlocksForTopic(tx, topic, 0, 200)
	if err != nil {
		t.Fatalf("BlocksForTopic failed: %v", err)
	}
	if len(blocks) != 1 || blocks[0] != 100 {
		t.Fatalf("expected [100], got %v", blocks)
	}
}

func TestWriteLogIndex_MultipleBlocks(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addr := types.HexToAddress("0x2222222222222222222222222222222222222222")
	topic1 := types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	topic2 := types.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")

	// Block 10: addr emits topic1
	receipts1 := makeReceipts([]*block.Log{makeLog(addr, topic1)})
	if err := WriteLogIndex(tx, 10, receipts1); err != nil {
		t.Fatalf("WriteLogIndex block 10 failed: %v", err)
	}

	// Block 20: addr emits topic2
	receipts2 := makeReceipts([]*block.Log{makeLog(addr, topic2)})
	if err := WriteLogIndex(tx, 20, receipts2); err != nil {
		t.Fatalf("WriteLogIndex block 20 failed: %v", err)
	}

	// Block 30: addr emits both topics
	receipts3 := makeReceipts([]*block.Log{makeLog(addr, topic1, topic2)})
	if err := WriteLogIndex(tx, 30, receipts3); err != nil {
		t.Fatalf("WriteLogIndex block 30 failed: %v", err)
	}

	// Address should appear in all three blocks.
	blocks, err := BlocksForAddress(tx, addr, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForAddress failed: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks for address, got %d: %v", len(blocks), blocks)
	}
	if blocks[0] != 10 || blocks[1] != 20 || blocks[2] != 30 {
		t.Fatalf("expected [10 20 30], got %v", blocks)
	}

	// topic1 should appear in blocks 10 and 30.
	blocks, err = BlocksForTopic(tx, topic1, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForTopic failed: %v", err)
	}
	if len(blocks) != 2 || blocks[0] != 10 || blocks[1] != 30 {
		t.Fatalf("expected [10 30], got %v", blocks)
	}

	// topic2 should appear in blocks 20 and 30.
	blocks, err = BlocksForTopic(tx, topic2, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForTopic failed: %v", err)
	}
	if len(blocks) != 2 || blocks[0] != 20 || blocks[1] != 30 {
		t.Fatalf("expected [20 30], got %v", blocks)
	}
}

func TestBlocksForAddress_Range(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addr := types.HexToAddress("0x3333333333333333333333333333333333333333")
	topic := types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")

	for _, num := range []uint64{5, 15, 25, 35, 45} {
		receipts := makeReceipts([]*block.Log{makeLog(addr, topic)})
		if err := WriteLogIndex(tx, num, receipts); err != nil {
			t.Fatalf("WriteLogIndex block %d failed: %v", num, err)
		}
	}

	// Query range [10, 30] should return blocks 15, 25.
	blocks, err := BlocksForAddress(tx, addr, 10, 30)
	if err != nil {
		t.Fatalf("BlocksForAddress range failed: %v", err)
	}
	if len(blocks) != 2 || blocks[0] != 15 || blocks[1] != 25 {
		t.Fatalf("expected [15 25], got %v", blocks)
	}
}

func TestBlocksForTopic_Range(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addr := types.HexToAddress("0x4444444444444444444444444444444444444444")
	topic := types.HexToHash("0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")

	for _, num := range []uint64{100, 200, 300, 400, 500} {
		receipts := makeReceipts([]*block.Log{makeLog(addr, topic)})
		if err := WriteLogIndex(tx, num, receipts); err != nil {
			t.Fatalf("WriteLogIndex block %d failed: %v", num, err)
		}
	}

	// Query range [150, 350] should return blocks 200 and 300.
	blocks, err := BlocksForTopic(tx, topic, 150, 350)
	if err != nil {
		t.Fatalf("BlocksForTopic range failed: %v", err)
	}
	if len(blocks) != 2 || blocks[0] != 200 || blocks[1] != 300 {
		t.Fatalf("expected [200 300], got %v", blocks)
	}
}

func TestBlocksForAddress_CrossShard(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addr := types.HexToAddress("0x5555555555555555555555555555555555555555")
	topic := types.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	// Write entries across what would be a shard boundary if there were many
	// entries. With ChunkLimit-based sharding, this tests that chunks are
	// properly merged when reading.
	blockNums := make([]uint64, 0, 200)
	for i := uint64(0); i < 200; i++ {
		num := i * 50 // 0, 50, 100, ..., 9950
		blockNums = append(blockNums, num)
		receipts := makeReceipts([]*block.Log{makeLog(addr, topic)})
		if err := WriteLogIndex(tx, num, receipts); err != nil {
			t.Fatalf("WriteLogIndex block %d failed: %v", num, err)
		}
	}

	// Query full range.
	blocks, err := BlocksForAddress(tx, addr, 0, 10000)
	if err != nil {
		t.Fatalf("BlocksForAddress cross-shard failed: %v", err)
	}
	if len(blocks) != 200 {
		t.Fatalf("expected 200 blocks, got %d", len(blocks))
	}
	for i, num := range blockNums {
		if blocks[i] != num {
			t.Fatalf("block[%d]: expected %d, got %d", i, num, blocks[i])
		}
	}

	// Query a sub-range.
	blocks, err = BlocksForAddress(tx, addr, 1000, 5000)
	if err != nil {
		t.Fatalf("BlocksForAddress sub-range failed: %v", err)
	}
	// Expected: 1000, 1050, 1100, ..., 5000 => (5000-1000)/50 + 1 = 81
	expected := 81
	if len(blocks) != expected {
		t.Fatalf("expected %d blocks in [1000, 5000], got %d", expected, len(blocks))
	}
}

func TestPruneLogIndex(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("BeginRw failed: %v", err)
	}
	defer tx.Rollback()

	addr := types.HexToAddress("0x6666666666666666666666666666666666666666")
	topic := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	// Write log index entries at blocks 10, 20, 30, 40, 50.
	for _, num := range []uint64{10, 20, 30, 40, 50} {
		receipts := makeReceipts([]*block.Log{makeLog(addr, topic)})
		if err := WriteLogIndex(tx, num, receipts); err != nil {
			t.Fatalf("WriteLogIndex block %d failed: %v", num, err)
		}
	}

	// Verify all 5 blocks present.
	blocks, err := BlocksForAddress(tx, addr, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForAddress failed: %v", err)
	}
	if len(blocks) != 5 {
		t.Fatalf("expected 5 blocks before prune, got %d", len(blocks))
	}

	// Prune blocks < 25.
	if err := PruneLogIndex(tx, 25); err != nil {
		t.Fatalf("PruneLogIndex failed: %v", err)
	}

	// Only blocks 30, 40, 50 should remain for address.
	blocks, err = BlocksForAddress(tx, addr, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForAddress after prune failed: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks after prune, got %d: %v", len(blocks), blocks)
	}
	if blocks[0] != 30 || blocks[1] != 40 || blocks[2] != 50 {
		t.Fatalf("expected [30 40 50], got %v", blocks)
	}

	// Same for topic.
	blocks, err = BlocksForTopic(tx, topic, 0, 100)
	if err != nil {
		t.Fatalf("BlocksForTopic after prune failed: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 topic blocks after prune, got %d: %v", len(blocks), blocks)
	}
}
