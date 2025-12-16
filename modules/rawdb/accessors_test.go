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
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Key Generation Tests (避免与 schema_test.go 重复)
// =============================================================================

func TestHeaderKeyGeneration(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	number := uint64(12345)

	key := HeaderKey(number, hash)

	if len(key) == 0 {
		t.Error("HeaderKey should return non-empty key")
	}

	// 测试 key 一致性
	key2 := HeaderKey(number, hash)
	if !bytes.Equal(key, key2) {
		t.Error("HeaderKey should be deterministic")
	}

	t.Logf("✓ HeaderKey generation: %d bytes", len(key))
}

func TestBlockBodyKeyGeneration(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	number := uint64(12345)

	key := BlockBodyKey(number, hash)

	if len(key) == 0 {
		t.Error("BlockBodyKey should return non-empty key")
	}

	t.Logf("✓ BlockBodyKey generation: %d bytes", len(key))
}

func TestTxLookupKeyGeneration(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

	key := TxLookupKey(hash)

	if len(key) == 0 {
		t.Error("TxLookupKey should return non-empty key")
	}

	t.Logf("✓ TxLookupKey generation: %d bytes", len(key))
}

func TestReceiptKeyGeneration(t *testing.T) {
	number := uint64(12345)

	key := ReceiptKey(number)

	if len(key) == 0 {
		t.Error("ReceiptKey should return non-empty key")
	}

	t.Logf("✓ ReceiptKey generation: %d bytes", len(key))
}

// =============================================================================
// Key Consistency Tests
// =============================================================================

func TestKeyConsistency(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	number := uint64(12345)

	// 相同输入应产生相同输出
	key1 := HeaderKey(number, hash)
	key2 := HeaderKey(number, hash)

	if !bytes.Equal(key1, key2) {
		t.Error("HeaderKey should be deterministic")
	}

	// 不同输入应产生不同输出
	key3 := HeaderKey(number+1, hash)
	if bytes.Equal(key1, key3) {
		t.Error("Different inputs should produce different keys")
	}

	t.Logf("✓ Key generation is consistent and deterministic")
}

func TestKeyUniqueness(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	number := uint64(12345)

	// HeaderKey 和 BlockBodyKey 使用相同格式（不同表/bucket）
	headerKey := HeaderKey(number, hash)
	bodyKey := BlockBodyKey(number, hash)
	txKey := TxLookupKey(hash)
	receiptKey := ReceiptKey(number)

	// 验证 HeaderKey 和 BlockBodyKey 格式相同（设计如此）
	if !bytes.Equal(headerKey, bodyKey) {
		t.Error("HeaderKey and BlockBodyKey should have same format")
	}

	// 验证不同类型的 key 格式不同
	if bytes.Equal(headerKey, txKey) {
		t.Error("HeaderKey and TxLookupKey should be different")
	}
	if bytes.Equal(txKey, receiptKey) {
		t.Error("TxLookupKey and ReceiptKey should be different")
	}

	t.Logf("✓ Key types have correct uniqueness properties")
}

// =============================================================================
// Encoding Tests
// =============================================================================

func TestEncodeBlockNumberConsistency(t *testing.T) {
	tests := []struct {
		name   string
		number uint64
	}{
		{"zero", 0},
		{"one", 1},
		{"small", 100},
		{"large", 1000000},
		{"max", ^uint64(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeBlockNumber(tt.number)

			if len(encoded) != 8 {
				t.Errorf("EncodeBlockNumber should return 8 bytes, got %d", len(encoded))
			}

			// 验证一致性
			encoded2 := EncodeBlockNumber(tt.number)
			if !bytes.Equal(encoded, encoded2) {
				t.Error("EncodeBlockNumber should be deterministic")
			}
		})
	}

	t.Logf("✓ EncodeBlockNumber works correctly")
}

// =============================================================================
// Uint256 Tests
// =============================================================================

func TestUint256Encoding(t *testing.T) {
	tests := []struct {
		name  string
		value *uint256.Int
	}{
		{"zero", uint256.NewInt(0)},
		{"one", uint256.NewInt(1)},
		{"small", uint256.NewInt(1000)},
		{"large", uint256.NewInt(1000000000000000000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// uint256 应该可以转换为 bytes
			bytesVal := tt.value.Bytes()
			if bytesVal == nil {
				t.Error("uint256.Bytes() should not return nil")
			}
		})
	}

	t.Logf("✓ uint256 encoding works correctly")
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestZeroHash(t *testing.T) {
	zeroHash := types.Hash{}

	key := TxLookupKey(zeroHash)
	if len(key) == 0 {
		t.Error("TxLookupKey should handle zero hash")
	}

	t.Logf("✓ Zero hash handled correctly")
}

func TestZeroNumber(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

	key := HeaderKey(0, hash)
	if len(key) == 0 {
		t.Error("HeaderKey should handle zero number")
	}

	t.Logf("✓ Zero number handled correctly")
}

func TestMaxNumber(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	maxNum := ^uint64(0)

	key := HeaderKey(maxNum, hash)
	if len(key) == 0 {
		t.Error("HeaderKey should handle max number")
	}

	t.Logf("✓ Max number handled correctly")
}

// =============================================================================
// Key Generation Consistency Tests
// =============================================================================

func TestKeyGenerationStability(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	number := uint64(12345)

	// 生成多个 key 验证稳定性
	for i := 0; i < 100; i++ {
		key1 := HeaderKey(number, hash)
		key2 := HeaderKey(number, hash)
		if !bytes.Equal(key1, key2) {
			t.Error("Key generation should be stable")
		}
	}

	t.Logf("✓ Key generation is stable across iterations")
}

func TestKeyBoundaryConditions(t *testing.T) {
	testCases := []struct {
		name   string
		number uint64
	}{
		{"genesis", 0},
		{"first", 1},
		{"small", 100},
		{"medium", 1000000},
		{"large", 100000000000},
		{"max-1", ^uint64(0) - 1},
		{"max", ^uint64(0)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
			key := HeaderKey(tc.number, hash)
			if len(key) == 0 {
				t.Errorf("HeaderKey failed for number=%d", tc.number)
			}
		})
	}

	t.Logf("✓ Key generation handles boundary conditions")
}
