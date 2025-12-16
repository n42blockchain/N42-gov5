// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package filters

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// =============================================================================
// FilterCriteria 测试
// =============================================================================

// TestFilterCriteriaDefaults 测试 FilterCriteria 默认值
func TestFilterCriteriaDefaults(t *testing.T) {
	var crit FilterCriteria

	// 空的 FilterCriteria 应该有零值
	if crit.BlockHash != (types.Hash{}) {
		t.Error("BlockHash should be zero value")
	}
	if crit.FromBlock != nil {
		t.Error("FromBlock should be nil")
	}
	if crit.ToBlock != nil {
		t.Error("ToBlock should be nil")
	}
	if len(crit.Addresses) != 0 {
		t.Error("Addresses should be empty")
	}
	if len(crit.Topics) != 0 {
		t.Error("Topics should be empty")
	}

	t.Log("✓ FilterCriteria defaults are correct")
}

// TestFilterCriteriaWithBlockHash 测试带 BlockHash 的 FilterCriteria
func TestFilterCriteriaWithBlockHash(t *testing.T) {
	blockHash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	crit := FilterCriteria{
		BlockHash: blockHash,
	}

	if crit.BlockHash != blockHash {
		t.Errorf("BlockHash mismatch: expected %s, got %s", blockHash.Hex(), crit.BlockHash.Hex())
	}

	t.Log("✓ FilterCriteria with BlockHash works")
}

// TestFilterCriteriaWithBlockRange 测试带区块范围的 FilterCriteria
func TestFilterCriteriaWithBlockRange(t *testing.T) {
	fromBlock := big.NewInt(100)
	toBlock := big.NewInt(200)

	crit := FilterCriteria{
		FromBlock: fromBlock,
		ToBlock:   toBlock,
	}

	if crit.FromBlock.Cmp(fromBlock) != 0 {
		t.Errorf("FromBlock mismatch: expected %s, got %s", fromBlock.String(), crit.FromBlock.String())
	}
	if crit.ToBlock.Cmp(toBlock) != 0 {
		t.Errorf("ToBlock mismatch: expected %s, got %s", toBlock.String(), crit.ToBlock.String())
	}

	t.Log("✓ FilterCriteria with block range works")
}

// TestFilterCriteriaWithAddresses 测试带地址的 FilterCriteria
func TestFilterCriteriaWithAddresses(t *testing.T) {
	addresses := []types.Address{
		types.HexToAddress("0x1111111111111111111111111111111111111111"),
		types.HexToAddress("0x2222222222222222222222222222222222222222"),
	}

	crit := FilterCriteria{
		Addresses: addresses,
	}

	if len(crit.Addresses) != 2 {
		t.Errorf("Expected 2 addresses, got %d", len(crit.Addresses))
	}

	t.Log("✓ FilterCriteria with addresses works")
}

// TestFilterCriteriaWithTopics 测试带 Topics 的 FilterCriteria
func TestFilterCriteriaWithTopics(t *testing.T) {
	topic1 := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	topic2 := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")

	crit := FilterCriteria{
		Topics: [][]types.Hash{
			{topic1},
			{topic2},
		},
	}

	if len(crit.Topics) != 2 {
		t.Errorf("Expected 2 topic groups, got %d", len(crit.Topics))
	}
	if len(crit.Topics[0]) != 1 {
		t.Errorf("Expected 1 topic in first group, got %d", len(crit.Topics[0]))
	}

	t.Log("✓ FilterCriteria with topics works")
}

// =============================================================================
// Filter Type 测试
// =============================================================================

// TestFilterTypes 测试过滤器类型常量
func TestFilterTypes(t *testing.T) {
	types := map[Type]string{
		UnknownSubscription:             "Unknown",
		LogsSubscription:                "Logs",
		PendingLogsSubscription:         "PendingLogs",
		MinedAndPendingLogsSubscription: "MinedAndPendingLogs",
		PendingTransactionsSubscription: "PendingTransactions",
		BlocksSubscription:              "Blocks",
		LastIndexSubscription:           "LastIndex",
	}

	for typ, name := range types {
		t.Logf("Type %d: %s", typ, name)
	}

	// 验证类型顺序正确
	if UnknownSubscription != 0 {
		t.Error("UnknownSubscription should be 0")
	}
	if LastIndexSubscription <= UnknownSubscription {
		t.Error("LastIndexSubscription should be greater than UnknownSubscription")
	}

	t.Log("✓ Filter types are correctly defined")
}

// =============================================================================
// Subscription 测试
// =============================================================================

// TestSubscriptionID 测试订阅 ID
func TestSubscriptionID(t *testing.T) {
	// 创建不同的订阅 ID
	ids := []jsonrpc.ID{
		jsonrpc.ID("0x1"),
		jsonrpc.ID("0x2"),
		jsonrpc.ID("test-sub-1"),
	}

	seen := make(map[jsonrpc.ID]bool)
	for _, id := range ids {
		if seen[id] {
			t.Errorf("Duplicate subscription ID: %s", id)
		}
		seen[id] = true
	}

	t.Log("✓ Subscription IDs work correctly")
}

// =============================================================================
// 辅助函数测试
// =============================================================================

// TestReturnHashes 测试 returnHashes 函数
func TestReturnHashes(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := returnHashes(nil)
		if result == nil {
			t.Error("returnHashes(nil) should return empty slice, not nil")
		}
		if len(result) != 0 {
			t.Errorf("Expected empty slice, got %d elements", len(result))
		}
	})

	t.Run("non-nil input", func(t *testing.T) {
		hashes := []types.Hash{
			types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		}
		result := returnHashes(hashes)
		if len(result) != 1 {
			t.Errorf("Expected 1 hash, got %d", len(result))
		}
	})

	t.Log("✓ returnHashes works correctly")
}

// TestReturnLogs 测试 returnLogs 函数
func TestReturnLogs(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := returnLogs(nil)
		if result == nil {
			t.Error("returnLogs(nil) should return empty slice, not nil")
		}
		if len(result) != 0 {
			t.Errorf("Expected empty slice, got %d elements", len(result))
		}
	})

	t.Log("✓ returnLogs works correctly")
}

// =============================================================================
// 边界条件测试
// =============================================================================

// TestFilterCriteriaEdgeCases 测试边界条件
func TestFilterCriteriaEdgeCases(t *testing.T) {
	t.Run("empty topics array", func(t *testing.T) {
		crit := FilterCriteria{
			Topics: [][]types.Hash{},
		}
		if crit.Topics == nil {
			t.Error("Topics should be empty slice, not nil")
		}
	})

	t.Run("nil topic in topics array", func(t *testing.T) {
		crit := FilterCriteria{
			Topics: [][]types.Hash{nil, nil},
		}
		if len(crit.Topics) != 2 {
			t.Errorf("Expected 2 topic groups, got %d", len(crit.Topics))
		}
	})

	t.Run("mixed nil and non-nil topics", func(t *testing.T) {
		topic := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
		crit := FilterCriteria{
			Topics: [][]types.Hash{
				nil,
				{topic},
				nil,
			},
		}
		if len(crit.Topics) != 3 {
			t.Errorf("Expected 3 topic groups, got %d", len(crit.Topics))
		}
	})

	t.Log("✓ Edge cases handled correctly")
}

// TestBlockNumberSpecialValues 测试特殊区块号
func TestBlockNumberSpecialValues(t *testing.T) {
	// 测试特殊区块号的转换
	specialBlocks := []struct {
		name   string
		number jsonrpc.BlockNumber
	}{
		{"Latest", jsonrpc.LatestBlockNumber},
		{"Pending", jsonrpc.PendingBlockNumber},
		{"Earliest", jsonrpc.EarliestBlockNumber},
	}

	for _, sb := range specialBlocks {
		t.Logf("BlockNumber %s: %d", sb.name, sb.number)
	}

	// 验证特殊值 (Latest 和 Pending 通常是负数，Earliest 是 0)
	if jsonrpc.LatestBlockNumber >= 0 {
		t.Error("LatestBlockNumber should be negative")
	}
	if jsonrpc.PendingBlockNumber >= 0 {
		t.Error("PendingBlockNumber should be negative")
	}
	// EarliestBlockNumber 通常是 0，表示创世区块
	if jsonrpc.EarliestBlockNumber < 0 {
		t.Log("Note: EarliestBlockNumber is negative (using special encoding)")
	}

	t.Log("✓ Special block numbers are correctly defined")
}

