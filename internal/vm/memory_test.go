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

// Reference: go-ethereum/core/vm/memory_test.go

package vm

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
)

func TestMemoryNew(t *testing.T) {
	mem := NewMemory()
	if mem == nil {
		t.Fatal("NewMemory returned nil")
	}
	if mem.Len() != 0 {
		t.Errorf("new memory Len() = %d, want 0", mem.Len())
	}
	if cap(mem.store) < 4*1024 {
		t.Errorf("initial capacity = %d, want >= 4096", cap(mem.store))
	}
}

func TestMemoryResize(t *testing.T) {
	tests := []struct {
		name string
		size uint64
	}{
		{"resize_to_zero", 0},
		{"resize_to_32", 32},
		{"resize_to_64", 64},
		{"resize_to_1024", 1024},
		{"resize_to_4096", 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := NewMemory()
			mem.Resize(tt.size)
			if got := mem.Len(); got != int(tt.size) {
				t.Errorf("after Resize(%d), Len() = %d", tt.size, got)
			}
		})
	}
}

func TestMemoryResizeDoesNotShrink(t *testing.T) {
	mem := NewMemory()

	mem.Resize(32)
	if mem.Len() != 32 {
		t.Errorf("first resize: Len() = %d, want 32", mem.Len())
	}

	mem.Resize(64)
	if mem.Len() != 64 {
		t.Errorf("larger resize: Len() = %d, want 64", mem.Len())
	}

	mem.Resize(32)
	if mem.Len() != 64 {
		t.Errorf("smaller resize should not shrink: Len() = %d, want 64", mem.Len())
	}
}

func TestMemorySet(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	data := []byte{0x01, 0x02, 0x03, 0x04}
	if err := mem.Set(0, uint64(len(data)), data); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	result := mem.GetCopy(0, int64(len(data)))
	if !bytes.Equal(result, data) {
		t.Errorf("Set data = %x, want %x", result, data)
	}

	if err := mem.Set(32, uint64(len(data)), data); err != nil {
		t.Fatalf("Set at offset failed: %v", err)
	}
	result = mem.GetCopy(32, int64(len(data)))
	if !bytes.Equal(result, data) {
		t.Errorf("Set at offset = %x, want %x", result, data)
	}
}

func TestMemorySetZeroSize(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	if err := mem.Set(100, 0, []byte{0x01, 0x02}); err != nil {
		t.Fatalf("Set with zero size failed: %v", err)
	}
	if mem.Len() != 32 {
		t.Errorf("zero-size Set changed length: got %d, want 32", mem.Len())
	}
}

func TestMemorySet32(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	val := uint256.NewInt(0x12345678)
	if err := mem.Set32(0, val); err != nil {
		t.Fatalf("Set32 failed: %v", err)
	}

	data := mem.GetPtr(0, 32)
	if data == nil {
		t.Fatal("GetPtr returned nil")
	}

	expected := make([]byte, 32)
	val.WriteToSlice(expected)
	if !bytes.Equal(data, expected) {
		t.Errorf("Set32 = %x, want %x", data, expected)
	}
}

func TestMemoryGetCopy(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	data := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	if err := mem.Set(10, uint64(len(data)), data); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	copy1 := mem.GetCopy(10, 4)
	copy2 := mem.GetCopy(10, 4)

	// Modifying one copy should not affect the other.
	copy1[0] = 0xFF
	if copy2[0] != 0xAA {
		t.Error("GetCopy should return independent copies")
	}
}

func TestMemoryGetCopyZeroSize(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	if result := mem.GetCopy(0, 0); result != nil {
		t.Error("GetCopy with size 0 should return nil")
	}
}

func TestMemoryGetCopyBeyondEnd(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	result := mem.GetCopy(100, 10)
	if result != nil && len(result) != 0 {
		t.Errorf("GetCopy beyond end: len = %d, want 0", len(result))
	}
}

func TestMemoryGetPtr(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	data := []byte{0x11, 0x22, 0x33, 0x44}
	if err := mem.Set(0, uint64(len(data)), data); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	ptr := mem.GetPtr(0, 4)
	if !bytes.Equal(ptr, data) {
		t.Errorf("GetPtr = %x, want %x", ptr, data)
	}

	// Modifying through ptr should modify internal storage.
	ptr[0] = 0xFF
	if mem.GetPtr(0, 4)[0] != 0xFF {
		t.Error("GetPtr should return reference to internal storage")
	}
}

func TestMemoryGetPtrZeroSize(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	if result := mem.GetPtr(0, 0); result != nil {
		t.Error("GetPtr with size 0 should return nil")
	}
}

func TestMemoryData(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	data := mem.Data()
	if len(data) != 32 {
		t.Errorf("Data() length = %d, want 32", len(data))
	}

	// Data() returns a reference to internal storage.
	data[0] = 0xAB
	if mem.Data()[0] != 0xAB {
		t.Error("Data() should return internal storage")
	}
}

func TestMemoryCopy(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	srcData := []byte{0x01, 0x02, 0x03, 0x04}
	if err := mem.Set(0, uint64(len(srcData)), srcData); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	mem.Copy(32, 0, 4)

	dstData := mem.GetCopy(32, 4)
	if !bytes.Equal(dstData, srcData) {
		t.Errorf("Copy result = %x, want %x", dstData, srcData)
	}
}

func TestMemoryCopyOverlapping(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	if err := mem.Set(0, uint64(len(data)), data); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Copy overlapping region (src=0, dst=2, len=4).
	mem.Copy(2, 0, 4)

	expected := []byte{0x01, 0x02, 0x01, 0x02, 0x03, 0x04, 0x07, 0x08}
	result := mem.GetCopy(0, 8)
	if !bytes.Equal(result, expected) {
		t.Errorf("overlapping copy = %x, want %x", result, expected)
	}
}

func TestMemoryCopyZeroLength(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	data := []byte{0x01, 0x02, 0x03, 0x04}
	if err := mem.Set(0, uint64(len(data)), data); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	mem.Copy(16, 0, 0)

	result := mem.GetCopy(0, 4)
	if !bytes.Equal(result, data) {
		t.Error("zero-length copy modified source data")
	}

	dst := mem.GetCopy(16, 4)
	if !bytes.Equal(dst, make([]byte, 4)) {
		t.Error("zero-length copy modified destination")
	}
}

func TestMemoryReset(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)
	_ = mem.Set(0, 32, make([]byte, 32))

	mem.Reset()

	if mem.Len() != 0 {
		t.Errorf("after Reset, Len() = %d, want 0", mem.Len())
	}
	if mem.lastGasCost != 0 {
		t.Errorf("after Reset, lastGasCost = %d, want 0", mem.lastGasCost)
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkMemoryResize(b *testing.B) {
	for i := 0; i < b.N; i++ {
		mem := NewMemory()
		mem.Resize(1024)
	}
}

func BenchmarkMemoryResizeLarge(b *testing.B) {
	for i := 0; i < b.N; i++ {
		mem := NewMemory()
		mem.Resize(64 * 1024)
	}
}

func BenchmarkMemorySet(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)
	data := make([]byte, 32)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mem.Set(0, 32, data)
	}
}

func BenchmarkMemorySet32(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)
	val := uint256.NewInt(12345678)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mem.Set32(0, val)
	}
}

func BenchmarkMemoryGetCopy(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)
	_ = mem.Set(0, 32, make([]byte, 32))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.GetCopy(0, 32)
	}
}

func BenchmarkMemoryGetPtr(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)
	_ = mem.Set(0, 32, make([]byte, 32))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.GetPtr(0, 32)
	}
}

func BenchmarkMemoryCopy(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)
	_ = mem.Set(0, 32, make([]byte, 32))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.Copy(512, 0, 32)
	}
}

func BenchmarkMemoryReset(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.Reset()
		mem.Resize(1024)
	}
}
