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

// Tests adapted from go-ethereum and erigon VM test suites.

package vm

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
)

// =============================================================================
// Memory Tests (Reference: go-ethereum/core/vm/memory_test.go)
// =============================================================================

func TestMemoryNew(t *testing.T) {
	mem := NewMemory()
	if mem == nil {
		t.Fatal("NewMemory returned nil")
	}
	if mem.Len() != 0 {
		t.Errorf("New memory should be empty, got len %d", mem.Len())
	}
	if cap(mem.store) < 4*1024 {
		t.Errorf("Initial capacity should be at least 4KB, got %d", cap(mem.store))
	}
	t.Logf("✓ NewMemory creates empty memory with initial capacity")
}

func TestMemoryResize(t *testing.T) {
	tests := []struct {
		name     string
		size     uint64
		expected int
	}{
		{"resize_to_zero", 0, 0},
		{"resize_to_32", 32, 32},
		{"resize_to_64", 64, 64},
		{"resize_to_1024", 1024, 1024},
		{"resize_to_4096", 4096, 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := NewMemory()
			mem.Resize(tt.size)
			if mem.Len() != tt.expected {
				t.Errorf("After Resize(%d), Len() = %d, want %d", tt.size, mem.Len(), tt.expected)
			}
		})
	}
	t.Logf("✓ Memory Resize works correctly")
}

func TestMemoryResizeMultiple(t *testing.T) {
	mem := NewMemory()

	// First resize
	mem.Resize(32)
	if mem.Len() != 32 {
		t.Errorf("First resize: expected len 32, got %d", mem.Len())
	}

	// Larger resize
	mem.Resize(64)
	if mem.Len() != 64 {
		t.Errorf("Second resize: expected len 64, got %d", mem.Len())
	}

	// Smaller resize (should not shrink)
	mem.Resize(32)
	if mem.Len() != 64 {
		t.Errorf("Smaller resize should not shrink: expected len 64, got %d", mem.Len())
	}

	t.Logf("✓ Memory resize handles multiple resizes correctly")
}

func TestMemorySet(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	// Set some data
	data := []byte{0x01, 0x02, 0x03, 0x04}
	mem.Set(0, uint64(len(data)), data)

	// Verify data was set
	result := mem.GetCopy(0, int64(len(data)))
	if !bytes.Equal(result, data) {
		t.Errorf("Set data mismatch: got %x, want %x", result, data)
	}

	// Set at offset
	mem.Set(32, uint64(len(data)), data)
	result = mem.GetCopy(32, int64(len(data)))
	if !bytes.Equal(result, data) {
		t.Errorf("Set at offset mismatch: got %x, want %x", result, data)
	}

	t.Logf("✓ Memory Set works correctly")
}

func TestMemorySetZeroSize(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	// Set with zero size should be no-op
	mem.Set(100, 0, []byte{0x01, 0x02})

	// Memory should remain unchanged
	if mem.Len() != 32 {
		t.Errorf("Zero-size set changed memory length: got %d, want 32", mem.Len())
	}

	t.Logf("✓ Memory Set with zero size is no-op")
}

func TestMemorySet32(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	// Set a uint256 value
	val := uint256.NewInt(0x12345678)
	mem.Set32(0, val)

	// Check that value was written correctly (right-padded/left-zeroed)
	data := mem.GetPtr(0, 32)
	if data == nil {
		t.Fatal("GetPtr returned nil")
	}

	// The value should be right-aligned in 32 bytes
	expected := make([]byte, 32)
	val.WriteToSlice(expected)
	if !bytes.Equal(data, expected) {
		t.Errorf("Set32 mismatch: got %x, want %x", data, expected)
	}

	t.Logf("✓ Memory Set32 works correctly")
}

func TestMemoryGetCopy(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	// Set some data
	data := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	mem.Set(10, uint64(len(data)), data)

	// GetCopy returns a copy, not a reference
	copy1 := mem.GetCopy(10, 4)
	copy2 := mem.GetCopy(10, 4)

	// Modify copy1
	copy1[0] = 0xFF

	// copy2 should be unchanged
	if copy2[0] != 0xAA {
		t.Error("GetCopy should return independent copies")
	}

	t.Logf("✓ Memory GetCopy returns independent copies")
}

func TestMemoryGetCopyZeroSize(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	result := mem.GetCopy(0, 0)
	if result != nil {
		t.Error("GetCopy with size 0 should return nil")
	}

	t.Logf("✓ Memory GetCopy with zero size returns nil")
}

func TestMemoryGetCopyBeyondEnd(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	// Request beyond memory end
	result := mem.GetCopy(100, 10)
	if result != nil && len(result) != 0 {
		t.Errorf("GetCopy beyond end should return empty/nil, got len %d", len(result))
	}

	t.Logf("✓ Memory GetCopy handles out-of-bounds correctly")
}

func TestMemoryGetPtr(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	data := []byte{0x11, 0x22, 0x33, 0x44}
	mem.Set(0, uint64(len(data)), data)

	// GetPtr returns a pointer to internal storage
	ptr := mem.GetPtr(0, 4)
	if !bytes.Equal(ptr, data) {
		t.Errorf("GetPtr mismatch: got %x, want %x", ptr, data)
	}

	// Modifying through ptr should modify internal storage
	ptr[0] = 0xFF
	ptr2 := mem.GetPtr(0, 4)
	if ptr2[0] != 0xFF {
		t.Error("GetPtr should return reference to internal storage")
	}

	t.Logf("✓ Memory GetPtr returns reference to internal storage")
}

func TestMemoryGetPtrZeroSize(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	result := mem.GetPtr(0, 0)
	if result != nil {
		t.Error("GetPtr with size 0 should return nil")
	}

	t.Logf("✓ Memory GetPtr with zero size returns nil")
}

func TestMemoryData(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	data := mem.Data()
	if len(data) != 32 {
		t.Errorf("Data() length mismatch: got %d, want 32", len(data))
	}

	// Should return internal storage
	data[0] = 0xAB
	internalData := mem.Data()
	if internalData[0] != 0xAB {
		t.Error("Data() should return internal storage")
	}

	t.Logf("✓ Memory Data returns internal storage")
}

func TestMemoryCopyBasic(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)

	// Set source data
	srcData := []byte{0x01, 0x02, 0x03, 0x04}
	mem.Set(0, uint64(len(srcData)), srcData)

	// Copy to destination
	mem.Copy(32, 0, 4)

	// Verify destination
	dstData := mem.GetCopy(32, 4)
	if !bytes.Equal(dstData, srcData) {
		t.Errorf("Copy mismatch: got %x, want %x", dstData, srcData)
	}

	t.Logf("✓ Memory Copy works correctly")
}

func TestMemoryCopyOverlapping(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	// Set initial data
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	mem.Set(0, uint64(len(data)), data)

	// Copy overlapping region (src=0, dst=2, len=4)
	// Should correctly handle Go's copy semantics
	mem.Copy(2, 0, 4)

	// After copy: [0x01, 0x02, 0x01, 0x02, 0x03, 0x04, 0x07, 0x08]
	expected := []byte{0x01, 0x02, 0x01, 0x02, 0x03, 0x04, 0x07, 0x08}
	result := mem.GetCopy(0, 8)
	if !bytes.Equal(result, expected) {
		t.Errorf("Overlapping copy mismatch: got %x, want %x", result, expected)
	}

	t.Logf("✓ Memory Copy handles overlapping regions correctly")
}

func TestMemoryCopyZeroLength(t *testing.T) {
	mem := NewMemory()
	mem.Resize(32)

	data := []byte{0x01, 0x02, 0x03, 0x04}
	mem.Set(0, uint64(len(data)), data)

	// Copy with zero length should be no-op
	mem.Copy(16, 0, 0)

	// Original data should be unchanged
	result := mem.GetCopy(0, 4)
	if !bytes.Equal(result, data) {
		t.Error("Zero-length copy modified source data")
	}

	// Destination should remain zero
	dst := mem.GetCopy(16, 4)
	expected := make([]byte, 4)
	if !bytes.Equal(dst, expected) {
		t.Error("Zero-length copy modified destination")
	}

	t.Logf("✓ Memory Copy with zero length is no-op")
}

func TestMemoryReset(t *testing.T) {
	mem := NewMemory()
	mem.Resize(64)
	mem.Set(0, 32, make([]byte, 32))

	mem.Reset()

	if mem.Len() != 0 {
		t.Errorf("After Reset, Len should be 0, got %d", mem.Len())
	}
	if mem.lastGasCost != 0 {
		t.Errorf("After Reset, lastGasCost should be 0, got %d", mem.lastGasCost)
	}

	t.Logf("✓ Memory Reset clears memory and gas cost")
}

// =============================================================================
// Memory Benchmark Tests
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
		mem.Set(0, 32, data)
	}
}

func BenchmarkMemorySet32(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)
	val := uint256.NewInt(12345678)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.Set32(0, val)
	}
}

func BenchmarkMemoryGetCopy(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)
	mem.Set(0, 32, make([]byte, 32))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.GetCopy(0, 32)
	}
}

func BenchmarkMemoryGetPtr(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)
	mem.Set(0, 32, make([]byte, 32))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mem.GetPtr(0, 32)
	}
}

func BenchmarkMemoryCopy(b *testing.B) {
	mem := NewMemory()
	mem.Resize(1024)
	mem.Set(0, 32, make([]byte, 32))

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

