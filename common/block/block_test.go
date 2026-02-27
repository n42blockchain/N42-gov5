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

	"github.com/n42blockchain/N42/common/types"
)

func TestBloomConstants(t *testing.T) {
	if BloomByteLength != 256 {
		t.Errorf("BloomByteLength = %d, want 256", BloomByteLength)
	}
	if BloomBitLength != 2048 {
		t.Errorf("BloomBitLength = %d, want 2048", BloomBitLength)
	}
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
}

func TestBytesToBloom(t *testing.T) {
	input := []byte{0x01, 0x02, 0x03, 0x04}
	bloom := BytesToBloom(input)

	if bloom[BloomByteLength-1] != 0x04 {
		t.Error("BytesToBloom did not set bytes correctly")
	}
}

func TestBloomAdd(t *testing.T) {
	var bloom Bloom
	bloom.Add([]byte("test data"))

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
}

func TestBloomTest(t *testing.T) {
	var bloom Bloom
	data := []byte("test topic")
	bloom.Add(data)

	if !bloom.Test(data) {
		t.Error("Bloom.Test should return true for added data")
	}
}

func TestBloomBig(t *testing.T) {
	input := make([]byte, 256)
	input[255] = 0xFF
	bloom := BytesToBloom(input)

	if bloom.Big() == nil {
		t.Error("Bloom.Big should not return nil")
	}
}

func TestBloomBytes(t *testing.T) {
	var bloom Bloom
	bloom[0] = 0x01
	bloom[255] = 0xFF

	b := bloom.Bytes()
	if len(b) != 256 {
		t.Errorf("Bloom.Bytes length = %d, want 256", len(b))
	}
	if b[0] != 0x01 || b[255] != 0xFF {
		t.Error("Bloom.Bytes content mismatch")
	}
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
	if !bytes.HasPrefix(text, []byte("0x")) {
		t.Error("Bloom.MarshalText should start with 0x")
	}
}

func TestBloom9(t *testing.T) {
	result := Bloom9([]byte{0x01, 0x02, 0x03})
	if len(result) != 256 {
		t.Errorf("Bloom9 result length = %d, want 256", len(result))
	}
}

func TestBloomLookup(t *testing.T) {
	var bloom Bloom
	topic := types.Hash{0x01, 0x02, 0x03}
	bloom.Add(topic.Bytes())

	if !BloomLookup(bloom, topic) {
		t.Error("BloomLookup should return true for added topic")
	}
}

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
}

func TestCreateBloomEmpty(t *testing.T) {
	bloom := CreateBloom(Receipts{})

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
}

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
