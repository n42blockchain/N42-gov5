// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for database schema documentation and key encoding.

package rawdb

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules"
)

// =============================================================================
// Key Encoding Tests
// =============================================================================

func TestEncodeBlockNumber(t *testing.T) {
	tests := []struct {
		number   uint64
		expected []byte
	}{
		{0, []byte{0, 0, 0, 0, 0, 0, 0, 0}},
		{1, []byte{0, 0, 0, 0, 0, 0, 0, 1}},
		{256, []byte{0, 0, 0, 0, 0, 0, 1, 0}},
		{0xFFFFFFFFFFFFFFFF, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
	}

	for _, tt := range tests {
		result := EncodeBlockNumber(tt.number)
		if !bytes.Equal(result, tt.expected) {
			t.Errorf("EncodeBlockNumber(%d) = %v, want %v", tt.number, result, tt.expected)
		}
	}
	t.Log("✓ EncodeBlockNumber works correctly")
}

func TestDecodeBlockNumber(t *testing.T) {
	tests := []struct {
		data     []byte
		expected uint64
	}{
		{[]byte{0, 0, 0, 0, 0, 0, 0, 0}, 0},
		{[]byte{0, 0, 0, 0, 0, 0, 0, 1}, 1},
		{[]byte{0, 0, 0, 0, 0, 0, 1, 0}, 256},
		{[]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, 0xFFFFFFFFFFFFFFFF},
	}

	for _, tt := range tests {
		result := DecodeBlockNumber(tt.data)
		if result != tt.expected {
			t.Errorf("DecodeBlockNumber(%v) = %d, want %d", tt.data, result, tt.expected)
		}
	}
	t.Log("✓ DecodeBlockNumber works correctly")
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	numbers := []uint64{0, 1, 100, 1000000, 0xFFFFFFFFFFFFFFFF}
	for _, n := range numbers {
		encoded := EncodeBlockNumber(n)
		decoded := DecodeBlockNumber(encoded)
		if decoded != n {
			t.Errorf("Round trip failed: %d -> %v -> %d", n, encoded, decoded)
		}
	}
	t.Log("✓ Encode/Decode round trip works correctly")
}

func TestHeaderKey(t *testing.T) {
	hash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	key := HeaderKey(100, hash)

	// Should be 8 bytes for number + 32 bytes for hash
	if len(key) != 40 {
		t.Errorf("HeaderKey length = %d, want 40", len(key))
	}

	// Check number encoding
	decodedNumber := DecodeBlockNumber(key[:8])
	if decodedNumber != 100 {
		t.Errorf("HeaderKey number = %d, want 100", decodedNumber)
	}

	// Check hash
	decodedHash := types.BytesToHash(key[8:])
	if decodedHash != hash {
		t.Errorf("HeaderKey hash = %s, want %s", decodedHash.Hex(), hash.Hex())
	}

	t.Log("✓ HeaderKey format is correct")
}

func TestBlockBodyKey(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	key := BlockBodyKey(200, hash)

	// Should be same format as HeaderKey
	if len(key) != 40 {
		t.Errorf("BlockBodyKey length = %d, want 40", len(key))
	}

	t.Log("✓ BlockBodyKey format is correct")
}

func TestTxLookupKey(t *testing.T) {
	hash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	key := TxLookupKey(hash)

	if len(key) != 32 {
		t.Errorf("TxLookupKey length = %d, want 32", len(key))
	}

	if !bytes.Equal(key, hash.Bytes()) {
		t.Errorf("TxLookupKey = %x, want %x", key, hash.Bytes())
	}

	t.Log("✓ TxLookupKey format is correct")
}

func TestReceiptKey(t *testing.T) {
	key := ReceiptKey(12345)

	if len(key) != 8 {
		t.Errorf("ReceiptKey length = %d, want 8", len(key))
	}

	decoded := DecodeBlockNumber(key)
	if decoded != 12345 {
		t.Errorf("ReceiptKey decoded = %d, want 12345", decoded)
	}

	t.Log("✓ ReceiptKey format is correct")
}

// =============================================================================
// Bucket Category Tests
// =============================================================================

func TestStateBucketsExist(t *testing.T) {
	for _, bucket := range StateBuckets {
		if bucket == "" {
			t.Error("Empty bucket name in StateBuckets")
		}
	}
	t.Logf("✓ StateBuckets contains %d buckets", len(StateBuckets))
}

func TestChainBucketsExist(t *testing.T) {
	for _, bucket := range ChainBuckets {
		if bucket == "" {
			t.Error("Empty bucket name in ChainBuckets")
		}
	}
	t.Logf("✓ ChainBuckets contains %d buckets", len(ChainBuckets))
}

func TestConsensusBucketsExist(t *testing.T) {
	for _, bucket := range ConsensusBuckets {
		if bucket == "" {
			t.Error("Empty bucket name in ConsensusBuckets")
		}
	}
	t.Logf("✓ ConsensusBuckets contains %d buckets", len(ConsensusBuckets))
}

func TestBucketCategoriesAreDisjoint(t *testing.T) {
	allBuckets := make(map[string]string)

	checkAndAdd := func(buckets []string, category string) {
		for _, b := range buckets {
			if existing, ok := allBuckets[b]; ok {
				t.Errorf("Bucket %q appears in both %s and %s", b, existing, category)
			}
			allBuckets[b] = category
		}
	}

	checkAndAdd(StateBuckets, "StateBuckets")
	checkAndAdd(ChainBuckets, "ChainBuckets")
	checkAndAdd(ConsensusBuckets, "ConsensusBuckets")
	checkAndAdd(MetadataBuckets, "MetadataBuckets")
	checkAndAdd(ApplicationBuckets, "ApplicationBuckets")

	t.Log("✓ All bucket categories are disjoint")
}

// =============================================================================
// Schema Consistency Tests
// =============================================================================

func TestAllBucketsInTableConfig(t *testing.T) {
	// Initialize the table config
	modules.N42Init()

	allCategorizedBuckets := make([]string, 0)
	allCategorizedBuckets = append(allCategorizedBuckets, StateBuckets...)
	allCategorizedBuckets = append(allCategorizedBuckets, ChainBuckets...)
	allCategorizedBuckets = append(allCategorizedBuckets, ConsensusBuckets...)
	allCategorizedBuckets = append(allCategorizedBuckets, MetadataBuckets...)
	allCategorizedBuckets = append(allCategorizedBuckets, ApplicationBuckets...)

	for _, bucket := range allCategorizedBuckets {
		if _, ok := modules.N42TableCfg[bucket]; !ok {
			t.Errorf("Bucket %q is not in AstTableCfg", bucket)
		}
	}

	t.Logf("✓ All %d categorized buckets are in AstTableCfg", len(allCategorizedBuckets))
}

func TestSchemaVersion(t *testing.T) {
	if SchemaVersion < 1 {
		t.Error("SchemaVersion should be >= 1")
	}
	if SchemaVersionKey == "" {
		t.Error("SchemaVersionKey should not be empty")
	}
	t.Logf("✓ Schema version: %d", SchemaVersion)
}

