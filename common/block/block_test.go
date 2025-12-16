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
