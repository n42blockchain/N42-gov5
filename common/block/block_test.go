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

package block

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Bloom Filter Tests
// =============================================================================

func TestBloomConstants(t *testing.T) {
	if BloomByteLength != 256 {
		t.Errorf("BloomByteLength = %d, want 256", BloomByteLength)
	}
	if BloomBitLength != 2048 {
		t.Errorf("BloomBitLength = %d, want 2048", BloomBitLength)
	}

	t.Logf("✓ Bloom constants are correct")
}

func TestBloomSetBytes(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{"empty", []byte{}},
		{"small", []byte{0x01, 0x02, 0x03}},
		{"exact_256", make([]byte, 256)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bloom Bloom
			bloom.SetBytes(tt.input)

			// Verify the bytes are set correctly (right-aligned)
			if len(tt.input) > 0 {
				offset := BloomByteLength - len(tt.input)
				for i, b := range tt.input {
					if bloom[offset+i] != b {
						t.Errorf("byte mismatch at %d", i)
					}
				}
			}
		})
	}

	t.Logf("✓ Bloom.SetBytes works correctly")
}

func TestBytesToBloom(t *testing.T) {
	input := []byte{0x01, 0x02, 0x03, 0x04}
	bloom := BytesToBloom(input)

	// Check last bytes
	if bloom[BloomByteLength-1] != 0x04 {
		t.Error("BytesToBloom did not set bytes correctly")
	}

	t.Logf("✓ BytesToBloom works correctly")
}

func TestBloomAdd(t *testing.T) {
	var bloom Bloom
	data := []byte("test data")

	bloom.Add(data)

	// Bloom should not be all zeros after adding
	allZero := true
	for _, b := range bloom {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("Bloom should not be all zeros after Add")
	}

	t.Logf("✓ Bloom.Add works correctly")
}

func TestBloomTest(t *testing.T) {
	var bloom Bloom
	data := []byte("test topic")

	// Before adding, Test should return false (with high probability)
	bloom.Add(data)

	// After adding, Test should return true
	if !bloom.Test(data) {
		t.Error("Bloom.Test should return true for added data")
	}

	t.Logf("✓ Bloom.Test works correctly")
}

func TestBloomBig(t *testing.T) {
	input := make([]byte, 256)
	input[255] = 0xFF
	bloom := BytesToBloom(input)

	bigVal := bloom.Big()
	if bigVal == nil {
		t.Error("Bloom.Big should not return nil")
	}

	t.Logf("✓ Bloom.Big works correctly")
}

func TestBloomBytes(t *testing.T) {
	var bloom Bloom
	bloom[0] = 0x01
	bloom[255] = 0xFF

	bytes := bloom.Bytes()
	if len(bytes) != 256 {
		t.Errorf("Bloom.Bytes length = %d, want 256", len(bytes))
	}
	if bytes[0] != 0x01 || bytes[255] != 0xFF {
		t.Error("Bloom.Bytes content mismatch")
	}

	t.Logf("✓ Bloom.Bytes works correctly")
}

func TestBloomMarshalText(t *testing.T) {
	var bloom Bloom
	bloom[255] = 0xFF

	text, err := bloom.MarshalText()
	if err != nil {
		t.Errorf("Bloom.MarshalText error: %v", err)
	}
	if len(text) == 0 {
		t.Error("Bloom.MarshalText returned empty string")
	}
	// Should start with "0x"
	if !bytes.HasPrefix(text, []byte("0x")) {
		t.Error("Bloom.MarshalText should start with 0x")
	}

	t.Logf("✓ Bloom.MarshalText works correctly")
}

func TestBloom9(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03}
	result := Bloom9(data)

	if len(result) != 256 {
		t.Errorf("Bloom9 result length = %d, want 256", len(result))
	}

	t.Logf("✓ Bloom9 works correctly")
}

func TestBloomLookup(t *testing.T) {
	var bloom Bloom
	topic := types.Hash{0x01, 0x02, 0x03}

	bloom.Add(topic.Bytes())

	if !BloomLookup(bloom, topic) {
		t.Error("BloomLookup should return true for added topic")
	}

	t.Logf("✓ BloomLookup works correctly")
}

// =============================================================================
// Log Tests
// =============================================================================

func TestLogFields(t *testing.T) {
	log := &Log{
		Address:     types.Address{0x01},
		Topics:      []types.Hash{{0x02}, {0x03}},
		Data:        []byte{0x04, 0x05},
		BlockNumber: uint256.NewInt(100),
		TxHash:      types.Hash{0x06},
		TxIndex:     1,
		BlockHash:   types.Hash{0x07},
		Index:       0,
		Removed:     false,
	}

	if log.Address[0] != 0x01 {
		t.Error("Log.Address mismatch")
	}
	if len(log.Topics) != 2 {
		t.Errorf("Log.Topics length = %d, want 2", len(log.Topics))
	}
	if len(log.Data) != 2 {
		t.Errorf("Log.Data length = %d, want 2", len(log.Data))
	}
	if log.BlockNumber.Uint64() != 100 {
		t.Error("Log.BlockNumber mismatch")
	}

	t.Logf("✓ Log fields work correctly")
}

func TestLogToProtoMessage(t *testing.T) {
	log := &Log{
		Address:     types.Address{0x01},
		Topics:      []types.Hash{{0x02}},
		Data:        []byte{0x03},
		BlockNumber: uint256.NewInt(100),
		TxHash:      types.Hash{0x04},
		TxIndex:     1,
		BlockHash:   types.Hash{0x05},
		Index:       0,
		Removed:     false,
	}

	proto := log.ToProtoMessage()
	if proto == nil {
		t.Error("Log.ToProtoMessage should not return nil")
	}

	t.Logf("✓ Log.ToProtoMessage works correctly")
}

func TestLogsType(t *testing.T) {
	logs := Logs{
		&Log{Address: types.Address{0x01}},
		&Log{Address: types.Address{0x02}},
	}

	if len(logs) != 2 {
		t.Errorf("Logs length = %d, want 2", len(logs))
	}

	t.Logf("✓ Logs type works correctly")
}

// =============================================================================
// BlockNonce Tests
// =============================================================================

func TestBlockNonceSize(t *testing.T) {
	var nonce BlockNonce
	if len(nonce) != 8 {
		t.Errorf("BlockNonce size = %d, want 8", len(nonce))
	}

	t.Logf("✓ BlockNonce size is correct")
}

// =============================================================================
// CreateBloom Tests
// =============================================================================

func TestCreateBloom(t *testing.T) {
	receipts := Receipts{
		&Receipt{
			Logs: []*Log{
				{
					Address: types.Address{0x01},
					Topics:  []types.Hash{{0x02}},
				},
			},
		},
	}

	bloom := CreateBloom(receipts)

	// Bloom should not be all zeros
	allZero := true
	for _, b := range bloom {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("CreateBloom should not return all zeros for non-empty receipts")
	}

	t.Logf("✓ CreateBloom works correctly")
}

func TestCreateBloomEmpty(t *testing.T) {
	receipts := Receipts{}
	bloom := CreateBloom(receipts)

	// Empty receipts should produce zero bloom
	allZero := true
	for _, b := range bloom {
		if b != 0 {
			allZero = false
			break
		}
	}
	if !allZero {
		t.Error("CreateBloom should return all zeros for empty receipts")
	}

	t.Logf("✓ CreateBloom handles empty receipts")
}

func TestLogsBloom(t *testing.T) {
	logs := []*Log{
		{
			Address: types.Address{0x01},
			Topics:  []types.Hash{{0x02}},
		},
	}

	bloomBytes := LogsBloom(logs)
	if len(bloomBytes) != 256 {
		t.Errorf("LogsBloom length = %d, want 256", len(bloomBytes))
	}

	t.Logf("✓ LogsBloom works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkBloomAdd(b *testing.B) {
	var bloom Bloom
	data := []byte("benchmark test data")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bloom.Add(data)
	}
}

func BenchmarkBloomTest(b *testing.B) {
	var bloom Bloom
	data := []byte("benchmark test data")
	bloom.Add(data)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bloom.Test(data)
	}
}

func BenchmarkBytesToBloom(b *testing.B) {
	input := make([]byte, 256)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BytesToBloom(input)
	}
}

func BenchmarkCreateBloomMultipleLogs(b *testing.B) {
	receipts := Receipts{
		&Receipt{
			Logs: []*Log{
				{Address: types.Address{0x01}, Topics: []types.Hash{{0x02}, {0x03}}},
				{Address: types.Address{0x04}, Topics: []types.Hash{{0x05}}},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CreateBloom(receipts)
	}
}

func BenchmarkBloomLookup(b *testing.B) {
	var bloom Bloom
	topic := types.Hash{0x01, 0x02, 0x03}
	bloom.Add(topic.Bytes())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BloomLookup(bloom, topic)
	}
}

func BenchmarkLogsBloom(b *testing.B) {
	logs := []*Log{
		{Address: types.Address{0x01}, Topics: []types.Hash{{0x02}, {0x03}}},
		{Address: types.Address{0x04}, Topics: []types.Hash{{0x05}}},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LogsBloom(logs)
	}
}

// =============================================================================
// BlockNonce Encode/Decode Tests
// =============================================================================

func TestEncodeNonce(t *testing.T) {
	tests := []struct {
		name  string
		input uint64
	}{
		{"zero", 0},
		{"one", 1},
		{"max_uint64", ^uint64(0)},
		{"typical", 12345678901234567890},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nonce := EncodeNonce(tt.input)
			result := nonce.Uint64()
			if result != tt.input {
				t.Errorf("EncodeNonce(%d).Uint64() = %d, want %d", tt.input, result, tt.input)
			}
		})
	}

	t.Logf("✓ EncodeNonce/Uint64 roundtrip works correctly")
}

func TestBlockNonceMarshalText(t *testing.T) {
	nonce := EncodeNonce(0x123456789ABCDEF0)

	text, err := nonce.MarshalText()
	if err != nil {
		t.Fatalf("BlockNonce.MarshalText error: %v", err)
	}
	if len(text) == 0 {
		t.Error("BlockNonce.MarshalText returned empty string")
	}
	// Should start with "0x"
	if !bytes.HasPrefix(text, []byte("0x")) {
		t.Error("BlockNonce.MarshalText should start with 0x")
	}

	t.Logf("✓ BlockNonce.MarshalText works correctly: %s", string(text))
}

func TestBlockNonceUnmarshalText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected uint64
	}{
		{"zeros", "0x0000000000000000", 0},
		{"ones", "0x0000000000000001", 1},
		{"full", "0xffffffffffffffff", ^uint64(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nonce BlockNonce
			err := nonce.UnmarshalText([]byte(tt.input))
			if err != nil {
				t.Fatalf("UnmarshalText error: %v", err)
			}
			if nonce.Uint64() != tt.expected {
				t.Errorf("UnmarshalText(%s) = %d, want %d", tt.input, nonce.Uint64(), tt.expected)
			}
		})
	}

	t.Logf("✓ BlockNonce.UnmarshalText works correctly")
}

func TestBlockNonceMarshalUnmarshalRoundtrip(t *testing.T) {
	original := EncodeNonce(0xDEADBEEFCAFEBABE)

	text, err := original.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error: %v", err)
	}

	var decoded BlockNonce
	err = decoded.UnmarshalText(text)
	if err != nil {
		t.Fatalf("UnmarshalText error: %v", err)
	}

	if original != decoded {
		t.Errorf("Roundtrip failed: original=%x, decoded=%x", original, decoded)
	}

	t.Logf("✓ BlockNonce marshal/unmarshal roundtrip works correctly")
}

// =============================================================================
// Header Tests
// =============================================================================

func TestHeaderNumber64(t *testing.T) {
	h := &Header{
		Number: uint256.NewInt(12345),
	}

	if h.Number64().Uint64() != 12345 {
		t.Errorf("Header.Number64() = %d, want 12345", h.Number64().Uint64())
	}

	t.Logf("✓ Header.Number64 works correctly")
}

func TestHeaderStateRoot(t *testing.T) {
	expectedRoot := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	h := &Header{
		Root:   expectedRoot,
		Number: uint256.NewInt(0),
	}

	if h.StateRoot() != expectedRoot {
		t.Errorf("Header.StateRoot() = %v, want %v", h.StateRoot(), expectedRoot)
	}

	t.Logf("✓ Header.StateRoot works correctly")
}

func TestHeaderBaseFee64(t *testing.T) {
	h := &Header{
		BaseFee: uint256.NewInt(1000000000),
		Number:  uint256.NewInt(0),
	}

	if h.BaseFee64().Uint64() != 1000000000 {
		t.Errorf("Header.BaseFee64() = %d, want 1000000000", h.BaseFee64().Uint64())
	}

	t.Logf("✓ Header.BaseFee64 works correctly")
}

func TestHeaderBaseFee64Nil(t *testing.T) {
	h := &Header{
		Number: uint256.NewInt(0),
	}

	if h.BaseFee64() != nil {
		t.Errorf("Header.BaseFee64() should be nil for header without BaseFee")
	}

	t.Logf("✓ Header.BaseFee64 handles nil correctly")
}

func TestHeaderHash(t *testing.T) {
	h := Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
		GasUsed:    21000,
		Time:       1234567890,
	}

	hash1 := h.Hash()
	if hash1 == (types.Hash{}) {
		t.Error("Header.Hash() should not return empty hash")
	}

	// Hash should be cached
	hash2 := h.Hash()
	if hash1 != hash2 {
		t.Error("Header.Hash() should return cached value")
	}

	t.Logf("✓ Header.Hash works correctly: %v", hash1)
}

func TestHeaderHashDifferentHeaders(t *testing.T) {
	h1 := Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
	}

	h2 := Header{
		ParentHash: types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
	}

	if h1.Hash() == h2.Hash() {
		t.Error("Different headers should have different hashes")
	}

	t.Logf("✓ Different headers produce different hashes")
}

func TestHeaderHashNilFields(t *testing.T) {
	// Test header with nil BaseFee and Difficulty (should be handled gracefully)
	h := Header{
		Number: uint256.NewInt(0),
	}

	hash := h.Hash()
	if hash == (types.Hash{}) {
		t.Error("Header.Hash() should not return empty hash even with nil fields")
	}

	t.Logf("✓ Header.Hash handles nil fields correctly")
}

func TestCopyHeader(t *testing.T) {
	original := &Header{
		ParentHash:  types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Coinbase:    types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Root:        types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		TxHash:      types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		ReceiptHash: types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Difficulty:  uint256.NewInt(1000000),
		Number:      uint256.NewInt(12345),
		GasLimit:    8000000,
		GasUsed:     5000000,
		Time:        1234567890,
		BaseFee:     uint256.NewInt(1000000000),
		Extra:       []byte("test extra data"),
		Nonce:       EncodeNonce(12345),
	}

	copied := CopyHeader(original)

	// Verify fields are equal
	if copied.ParentHash != original.ParentHash {
		t.Error("CopyHeader: ParentHash mismatch")
	}
	if copied.Coinbase != original.Coinbase {
		t.Error("CopyHeader: Coinbase mismatch")
	}
	if copied.Number.Cmp(original.Number) != 0 {
		t.Error("CopyHeader: Number mismatch")
	}
	if copied.Difficulty.Cmp(original.Difficulty) != 0 {
		t.Error("CopyHeader: Difficulty mismatch")
	}
	if copied.BaseFee.Cmp(original.BaseFee) != 0 {
		t.Error("CopyHeader: BaseFee mismatch")
	}
	if !bytes.Equal(copied.Extra, original.Extra) {
		t.Error("CopyHeader: Extra mismatch")
	}

	// Verify it's a deep copy (modifying copy doesn't affect original)
	copied.Number = uint256.NewInt(99999)
	if original.Number.Uint64() == 99999 {
		t.Error("CopyHeader should create a deep copy")
	}

	copied.Extra[0] = 0xFF
	if original.Extra[0] == 0xFF {
		t.Error("CopyHeader should deep copy Extra data")
	}

	t.Logf("✓ CopyHeader works correctly")
}

func TestCopyHeaderNilFields(t *testing.T) {
	original := &Header{
		Number: uint256.NewInt(100),
		// Difficulty, BaseFee, Extra are nil/empty
	}

	copied := CopyHeader(original)

	if copied.Number.Uint64() != 100 {
		t.Error("CopyHeader: Number mismatch")
	}

	t.Logf("✓ CopyHeader handles nil fields correctly")
}

func TestCopyReward(t *testing.T) {
	rewards := []*Reward{
		{
			Address: types.HexToAddress("0x1234567890123456789012345678901234567890"),
			Amount:  uint256.NewInt(1000000000000000000),
		},
		{
			Address: types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"),
			Amount:  uint256.NewInt(2000000000000000000),
		},
	}

	copied := CopyReward(rewards)

	if len(copied) != len(rewards) {
		t.Errorf("CopyReward length mismatch: got %d, want %d", len(copied), len(rewards))
	}

	for i := range rewards {
		if copied[i].Address != rewards[i].Address {
			t.Errorf("CopyReward[%d].Address mismatch", i)
		}
		if copied[i].Amount.Cmp(rewards[i].Amount) != 0 {
			t.Errorf("CopyReward[%d].Amount mismatch", i)
		}
	}

	// Verify deep copy
	copied[0].Amount = uint256.NewInt(999)
	if rewards[0].Amount.Cmp(uint256.NewInt(999)) == 0 {
		t.Error("CopyReward should create a deep copy")
	}

	t.Logf("✓ CopyReward works correctly")
}

func TestCopyRewardEmpty(t *testing.T) {
	rewards := []*Reward{}
	copied := CopyReward(rewards)

	if len(copied) != 0 {
		t.Errorf("CopyReward of empty slice should return empty, got length %d", len(copied))
	}

	t.Logf("✓ CopyReward handles empty slice correctly")
}

func TestHeaderMarshalUnmarshal(t *testing.T) {
	original := &Header{
		ParentHash:  types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Coinbase:    types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Root:        types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		TxHash:      types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		ReceiptHash: types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Difficulty:  uint256.NewInt(1000000),
		Number:      uint256.NewInt(12345),
		GasLimit:    8000000,
		GasUsed:     5000000,
		Time:        1234567890,
		BaseFee:     uint256.NewInt(1000000000),
		Extra:       []byte("test extra data"),
		Nonce:       EncodeNonce(12345),
		MixDigest:   types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333"),
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Header.Marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Header.Marshal returned empty data")
	}

	decoded := &Header{}
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Header.Unmarshal error: %v", err)
	}

	// Verify fields
	if decoded.ParentHash != original.ParentHash {
		t.Error("Marshal/Unmarshal: ParentHash mismatch")
	}
	if decoded.Coinbase != original.Coinbase {
		t.Error("Marshal/Unmarshal: Coinbase mismatch")
	}
	if decoded.Number.Cmp(original.Number) != 0 {
		t.Error("Marshal/Unmarshal: Number mismatch")
	}
	if decoded.GasLimit != original.GasLimit {
		t.Error("Marshal/Unmarshal: GasLimit mismatch")
	}
	if decoded.GasUsed != original.GasUsed {
		t.Error("Marshal/Unmarshal: GasUsed mismatch")
	}
	if decoded.Time != original.Time {
		t.Error("Marshal/Unmarshal: Time mismatch")
	}
	if decoded.Nonce.Uint64() != original.Nonce.Uint64() {
		t.Error("Marshal/Unmarshal: Nonce mismatch")
	}
	if !bytes.Equal(decoded.Extra, original.Extra) {
		t.Error("Marshal/Unmarshal: Extra mismatch")
	}

	t.Logf("✓ Header.Marshal/Unmarshal roundtrip works correctly")
}

func TestHeaderUnmarshalInvalidData(t *testing.T) {
	h := &Header{}
	err := h.Unmarshal([]byte{0x00, 0x01, 0x02}) // Invalid protobuf data

	if err == nil {
		t.Error("Header.Unmarshal should fail with invalid data")
	}

	t.Logf("✓ Header.Unmarshal correctly rejects invalid data")
}

func TestHeaderToProtoMessage(t *testing.T) {
	h := &Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
		Time:       1234567890,
		Difficulty: uint256.NewInt(1000),
		BaseFee:    uint256.NewInt(1000000000),
	}

	proto := h.ToProtoMessage()
	if proto == nil {
		t.Error("Header.ToProtoMessage should not return nil")
	}

	t.Logf("✓ Header.ToProtoMessage works correctly")
}

func TestHeaderFromProtoMessageTypeError(t *testing.T) {
	h := &Header{}
	// Pass wrong proto message type
	err := h.FromProtoMessage(nil)
	if err == nil {
		t.Error("Header.FromProtoMessage should fail with nil message")
	}

	t.Logf("✓ Header.FromProtoMessage correctly rejects nil message")
}

// =============================================================================
// Header Benchmark Tests
// =============================================================================

func BenchmarkHeaderHash(b *testing.B) {
	h := Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
		GasUsed:    21000,
		Time:       1234567890,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Clear cached hash to force recalculation
		h.hash.Store(nil)
		h.Hash()
	}
}

func BenchmarkCopyHeader(b *testing.B) {
	h := &Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		Difficulty: uint256.NewInt(1000000),
		BaseFee:    uint256.NewInt(1000000000),
		Extra:      make([]byte, 32),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CopyHeader(h)
	}
}

func BenchmarkHeaderMarshal(b *testing.B) {
	h := &Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
		Difficulty: uint256.NewInt(1000000),
		BaseFee:    uint256.NewInt(1000000000),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Marshal()
	}
}

func BenchmarkEncodeNonce(b *testing.B) {
	for i := 0; i < b.N; i++ {
		EncodeNonce(uint64(i))
	}
}

// =============================================================================
// Log Proto Tests
// =============================================================================

func TestLogFromProtoMessage(t *testing.T) {
	original := &Log{
		Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Topics:      []types.Hash{{0x01}, {0x02}},
		Data:        []byte{0x03, 0x04},
		BlockNumber: uint256.NewInt(100),
		TxHash:      types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		TxIndex:     1,
		BlockHash:   types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Index:       0,
		Removed:     false,
	}

	// Convert to proto
	protoMsg := original.ToProtoMessage()

	// Convert back
	decoded := &Log{}
	err := decoded.FromProtoMessage(protoMsg)
	if err != nil {
		t.Fatalf("Log.FromProtoMessage error: %v", err)
	}

	// Verify fields
	if decoded.Address != original.Address {
		t.Error("Log.FromProtoMessage: Address mismatch")
	}
	if len(decoded.Topics) != len(original.Topics) {
		t.Error("Log.FromProtoMessage: Topics length mismatch")
	}
	if !bytes.Equal(decoded.Data, original.Data) {
		t.Error("Log.FromProtoMessage: Data mismatch")
	}
	if decoded.TxIndex != original.TxIndex {
		t.Error("Log.FromProtoMessage: TxIndex mismatch")
	}
	if decoded.Index != original.Index {
		t.Error("Log.FromProtoMessage: Index mismatch")
	}
	if decoded.Removed != original.Removed {
		t.Error("Log.FromProtoMessage: Removed mismatch")
	}

	t.Logf("✓ Log.FromProtoMessage works correctly")
}

func TestLogFromProtoMessageTypeError(t *testing.T) {
	log := &Log{}
	err := log.FromProtoMessage(nil)
	if err == nil {
		t.Error("Log.FromProtoMessage should fail with nil message")
	}

	t.Logf("✓ Log.FromProtoMessage correctly rejects nil message")
}

func TestLogsMarshalUnmarshal(t *testing.T) {
	original := Logs{
		&Log{
			Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
			Topics:      []types.Hash{{0x01}},
			Data:        []byte{0x02},
			BlockNumber: uint256.NewInt(100),
			TxHash:      types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
			TxIndex:     1,
		},
		&Log{
			Address:     types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"),
			Topics:      []types.Hash{{0x03}, {0x04}},
			Data:        []byte{0x05, 0x06},
			BlockNumber: uint256.NewInt(101),
			TxIndex:     2,
		},
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Logs.Marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Logs.Marshal returned empty data")
	}

	// Test Unmarshal
	var decoded Logs
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Logs.Unmarshal error: %v", err)
	}

	if len(decoded) != len(original) {
		t.Fatalf("Logs.Unmarshal length mismatch: got %d, want %d", len(decoded), len(original))
	}

	// Verify first log
	if decoded[0].Address != original[0].Address {
		t.Error("Logs.Unmarshal: Address mismatch")
	}
	if decoded[0].TxIndex != original[0].TxIndex {
		t.Error("Logs.Unmarshal: TxIndex mismatch")
	}
	if !bytes.Equal(decoded[0].Data, original[0].Data) {
		t.Error("Logs.Unmarshal: Data mismatch")
	}

	t.Logf("✓ Logs.Marshal/Unmarshal roundtrip works correctly (size: %d bytes)", len(data))
}

func TestLogsUnmarshalEmpty(t *testing.T) {
	original := Logs{}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Logs.Marshal error: %v", err)
	}

	var decoded Logs
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Logs.Unmarshal error: %v", err)
	}

	if len(decoded) != 0 {
		t.Errorf("Logs.Unmarshal of empty logs should be empty, got %d", len(decoded))
	}

	t.Logf("✓ Logs.Unmarshal handles empty logs correctly")
}

func TestLogsUnmarshalInvalidData(t *testing.T) {
	var logs Logs
	err := logs.Unmarshal([]byte{0x00, 0x01, 0x02}) // Invalid protobuf

	if err == nil {
		t.Error("Logs.Unmarshal should fail with invalid data")
	}

	t.Logf("✓ Logs.Unmarshal correctly rejects invalid data")
}

// =============================================================================
// Receipt Tests
// =============================================================================

func TestReceiptStatusConstants(t *testing.T) {
	if ReceiptStatusFailed != 0 {
		t.Errorf("ReceiptStatusFailed = %d, want 0", ReceiptStatusFailed)
	}
	if ReceiptStatusSuccessful != 1 {
		t.Errorf("ReceiptStatusSuccessful = %d, want 1", ReceiptStatusSuccessful)
	}

	t.Logf("✓ Receipt status constants are correct")
}

func TestReceiptMarshalUnmarshal(t *testing.T) {
	original := &Receipt{
		Type:              0,
		PostState:         []byte{0x01},
		Status:            ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		Logs: []*Log{
			{
				Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
				Topics:      []types.Hash{{0x01}},
				Data:        []byte{0x02},
				BlockNumber: uint256.NewInt(100),
			},
		},
		TxHash:           types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		ContractAddress:  types.Address{},
		GasUsed:          21000,
		BlockHash:        types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		BlockNumber:      uint256.NewInt(100),
		TransactionIndex: 0,
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Receipt.Marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Receipt.Marshal returned empty data")
	}

	decoded := &Receipt{}
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Receipt.Unmarshal error: %v", err)
	}

	// Verify fields
	if decoded.Status != original.Status {
		t.Error("Receipt.Unmarshal: Status mismatch")
	}
	if decoded.CumulativeGasUsed != original.CumulativeGasUsed {
		t.Error("Receipt.Unmarshal: CumulativeGasUsed mismatch")
	}
	if decoded.GasUsed != original.GasUsed {
		t.Error("Receipt.Unmarshal: GasUsed mismatch")
	}
	if decoded.TransactionIndex != original.TransactionIndex {
		t.Error("Receipt.Unmarshal: TransactionIndex mismatch")
	}
	if len(decoded.Logs) != len(original.Logs) {
		t.Error("Receipt.Unmarshal: Logs length mismatch")
	}

	t.Logf("✓ Receipt.Marshal/Unmarshal roundtrip works correctly")
}

func TestReceiptUnmarshalInvalidData(t *testing.T) {
	r := &Receipt{}
	err := r.Unmarshal([]byte{0x00, 0x01, 0x02}) // Invalid protobuf

	if err == nil {
		t.Error("Receipt.Unmarshal should fail with invalid data")
	}

	t.Logf("✓ Receipt.Unmarshal correctly rejects invalid data")
}

func TestReceiptsLen(t *testing.T) {
	rs := Receipts{
		&Receipt{Status: ReceiptStatusSuccessful, BlockNumber: uint256.NewInt(1)},
		&Receipt{Status: ReceiptStatusFailed, BlockNumber: uint256.NewInt(1)},
		&Receipt{Status: ReceiptStatusSuccessful, BlockNumber: uint256.NewInt(1)},
	}

	if rs.Len() != 3 {
		t.Errorf("Receipts.Len() = %d, want 3", rs.Len())
	}

	t.Logf("✓ Receipts.Len works correctly")
}

func TestReceiptsEncodeIndex(t *testing.T) {
	rs := Receipts{
		&Receipt{
			Status:            ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			BlockNumber:       uint256.NewInt(100),
			Logs: []*Log{
				{
					Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
					Topics:      []types.Hash{{0x01}},
					Data:        []byte{0x02},
					BlockNumber: uint256.NewInt(100),
				},
			},
		},
	}

	var buf bytes.Buffer
	rs.EncodeIndex(0, &buf)

	if buf.Len() == 0 {
		t.Error("Receipts.EncodeIndex produced empty output")
	}

	t.Logf("✓ Receipts.EncodeIndex works correctly (size: %d bytes)", buf.Len())
}

func TestReceiptsMarshalUnmarshal(t *testing.T) {
	original := Receipts{
		&Receipt{
			Type:              0,
			Status:            ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			GasUsed:           21000,
			Logs: []*Log{
				{
					Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
					Topics:      []types.Hash{{0x01}},
					Data:        []byte{0x02},
					BlockNumber: uint256.NewInt(100),
				},
			},
			TxHash:      types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
			BlockNumber: uint256.NewInt(100),
		},
		&Receipt{
			Type:              1,
			Status:            ReceiptStatusFailed,
			CumulativeGasUsed: 42000,
			GasUsed:           21000,
			Logs:              []*Log{},
			TxHash:            types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
			BlockNumber:       uint256.NewInt(100),
		},
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Receipts.Marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Receipts.Marshal returned empty data")
	}

	decoded := &Receipts{}
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Receipts.Unmarshal error: %v", err)
	}

	if len(*decoded) != len(original) {
		t.Errorf("Receipts.Unmarshal length mismatch: got %d, want %d", len(*decoded), len(original))
	}

	t.Logf("✓ Receipts.Marshal/Unmarshal roundtrip works correctly")
}

func TestReceiptsUnmarshalInvalidData(t *testing.T) {
	rs := &Receipts{}
	err := rs.Unmarshal([]byte{0x00, 0x01, 0x02}) // Invalid protobuf

	if err == nil {
		t.Error("Receipts.Unmarshal should fail with invalid data")
	}

	t.Logf("✓ Receipts.Unmarshal correctly rejects invalid data")
}

func TestReceiptsToProtoMessage(t *testing.T) {
	rs := Receipts{
		&Receipt{
			Status:            ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			GasUsed:           21000,
			BlockNumber:       uint256.NewInt(100),
		},
	}

	proto := rs.ToProtoMessage()
	if proto == nil {
		t.Error("Receipts.ToProtoMessage should not return nil")
	}

	t.Logf("✓ Receipts.ToProtoMessage works correctly")
}

// =============================================================================
// Receipt Benchmark Tests
// =============================================================================

func BenchmarkReceiptMarshal(b *testing.B) {
	r := &Receipt{
		Status:            ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		GasUsed:           21000,
		Logs: []*Log{
			{
				Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
				Topics:      []types.Hash{{0x01}},
				Data:        []byte{0x02},
				BlockNumber: uint256.NewInt(100),
			},
		},
		BlockNumber: uint256.NewInt(100),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Marshal()
	}
}

func BenchmarkReceiptsEncodeIndex(b *testing.B) {
	rs := Receipts{
		&Receipt{
			Status:            ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			BlockNumber:       uint256.NewInt(100),
			Logs: []*Log{
				{
					Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
					Topics:      []types.Hash{{0x01}},
					Data:        []byte{0x02},
					BlockNumber: uint256.NewInt(100),
				},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		rs.EncodeIndex(0, &buf)
	}
}
