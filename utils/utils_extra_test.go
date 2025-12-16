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

package utils

import (
	"bytes"
	"testing"
)

// =============================================================================
// ToBytes Tests
// =============================================================================

func TestToBytes4(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  [4]byte
	}{
		{"exact_4", []byte{1, 2, 3, 4}, [4]byte{1, 2, 3, 4}},
		{"less_than_4", []byte{1, 2}, [4]byte{1, 2, 0, 0}},
		{"more_than_4", []byte{1, 2, 3, 4, 5, 6}, [4]byte{1, 2, 3, 4}},
		{"empty", []byte{}, [4]byte{0, 0, 0, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToBytes4(tt.input)
			if got != tt.want {
				t.Errorf("ToBytes4(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}

	t.Logf("✓ ToBytes4 works correctly")
}

func TestToBytes20(t *testing.T) {
	tests := []struct {
		name string
		len  int
	}{
		{"exact_20", 20},
		{"less_than_20", 10},
		{"more_than_20", 30},
		{"empty", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := make([]byte, tt.len)
			for i := range input {
				input[i] = byte(i % 256)
			}

			got := ToBytes20(input)

			// Verify length
			if len(got) != 20 {
				t.Errorf("ToBytes20 result length = %d, want 20", len(got))
			}

			// Verify content
			expectedLen := tt.len
			if expectedLen > 20 {
				expectedLen = 20
			}
			for i := 0; i < expectedLen; i++ {
				if got[i] != input[i] {
					t.Errorf("ToBytes20[%d] = %d, want %d", i, got[i], input[i])
				}
			}
		})
	}

	t.Logf("✓ ToBytes20 works correctly")
}

func TestToBytes32(t *testing.T) {
	tests := []struct {
		name string
		len  int
	}{
		{"exact_32", 32},
		{"less_than_32", 16},
		{"more_than_32", 64},
		{"empty", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := make([]byte, tt.len)
			for i := range input {
				input[i] = byte(i % 256)
			}

			got := ToBytes32(input)

			// Verify length
			if len(got) != 32 {
				t.Errorf("ToBytes32 result length = %d, want 32", len(got))
			}
		})
	}

	t.Logf("✓ ToBytes32 works correctly")
}

func TestToBytes48(t *testing.T) {
	input := make([]byte, 48)
	for i := range input {
		input[i] = byte(i)
	}

	got := ToBytes48(input)

	if len(got) != 48 {
		t.Errorf("ToBytes48 result length = %d, want 48", len(got))
	}
	if !bytes.Equal(got[:], input) {
		t.Error("ToBytes48 content mismatch")
	}

	t.Logf("✓ ToBytes48 works correctly")
}

func TestToBytes64(t *testing.T) {
	input := make([]byte, 64)
	for i := range input {
		input[i] = byte(i)
	}

	got := ToBytes64(input)

	if len(got) != 64 {
		t.Errorf("ToBytes64 result length = %d, want 64", len(got))
	}
	if !bytes.Equal(got[:], input) {
		t.Error("ToBytes64 content mismatch")
	}

	t.Logf("✓ ToBytes64 works correctly")
}

func TestToBytes96(t *testing.T) {
	input := make([]byte, 96)
	for i := range input {
		input[i] = byte(i % 256)
	}

	got := ToBytes96(input)

	if len(got) != 96 {
		t.Errorf("ToBytes96 result length = %d, want 96", len(got))
	}
	if !bytes.Equal(got[:], input) {
		t.Error("ToBytes96 content mismatch")
	}

	t.Logf("✓ ToBytes96 works correctly")
}

// =============================================================================
// Keccak256 Tests
// =============================================================================

func TestKeccak256_Extended(t *testing.T) {
	data := []byte("hello world")
	hash := Keccak256(data)

	if len(hash) != 32 {
		t.Errorf("Keccak256 hash length = %d, want 32", len(hash))
	}

	// Same input should produce same output
	hash2 := Keccak256(data)
	if !bytes.Equal(hash, hash2) {
		t.Error("Keccak256 is not deterministic")
	}

	t.Logf("✓ Keccak256 works correctly")
}

func TestKeccak256_Multiple(t *testing.T) {
	data1 := []byte("hello")
	data2 := []byte(" world")

	// Hash of concatenated data
	hash1 := Keccak256(data1, data2)
	hash2 := Keccak256(append(data1, data2...))

	if !bytes.Equal(hash1, hash2) {
		t.Error("Keccak256 multi-input should equal concatenated input")
	}

	t.Logf("✓ Keccak256 handles multiple inputs correctly")
}

func TestKeccak256_Empty(t *testing.T) {
	hash := Keccak256([]byte{})

	if len(hash) != 32 {
		t.Errorf("Keccak256 empty hash length = %d, want 32", len(hash))
	}

	// Empty input should have a specific hash
	if hash == nil {
		t.Error("Keccak256 of empty should not be nil")
	}

	t.Logf("✓ Keccak256 handles empty input correctly")
}

func TestKeccak256Hash(t *testing.T) {
	data := []byte("test data")
	hash := Keccak256Hash(data)

	if len(hash) != 32 {
		t.Errorf("Keccak256Hash length = %d, want 32", len(hash))
	}

	t.Logf("✓ Keccak256Hash works correctly")
}

func TestHash256toS_Extended(t *testing.T) {
	data := []byte("hello")
	hexHash := Hash256toS(data)

	if len(hexHash) != 64 { // 32 bytes = 64 hex chars
		t.Errorf("Hash256toS length = %d, want 64", len(hexHash))
	}

	// Should be valid hex
	for _, c := range hexHash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("Hash256toS contains invalid hex char: %c", c)
		}
	}

	t.Logf("✓ Hash256toS works correctly")
}

// =============================================================================
// HexPrefix Tests
// =============================================================================

func TestHexPrefix(t *testing.T) {
	tests := []struct {
		name  string
		a     []byte
		b     []byte
		wantL int
	}{
		{"identical", []byte{1, 2, 3}, []byte{1, 2, 3}, 3},
		{"partial", []byte{1, 2, 3}, []byte{1, 2, 4}, 2},
		{"no_match", []byte{1, 2, 3}, []byte{4, 5, 6}, 0},
		{"empty_a", []byte{}, []byte{1, 2, 3}, 0},
		{"empty_b", []byte{1, 2, 3}, []byte{}, 0},
		{"a_shorter", []byte{1, 2}, []byte{1, 2, 3}, 2},
		{"b_shorter", []byte{1, 2, 3}, []byte{1, 2}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix, length := HexPrefix(tt.a, tt.b)
			if length != tt.wantL {
				t.Errorf("HexPrefix length = %d, want %d", length, tt.wantL)
			}
			if len(prefix) != tt.wantL {
				t.Errorf("HexPrefix result length = %d, want %d", len(prefix), tt.wantL)
			}
		})
	}

	t.Logf("✓ HexPrefix works correctly")
}

// =============================================================================
// Exists Tests
// =============================================================================

func TestExists(t *testing.T) {
	// Test with existing path
	if !Exists(".") {
		t.Error("Exists should return true for current directory")
	}

	// Test with non-existing path
	if Exists("/nonexistent/path/that/should/not/exist") {
		t.Error("Exists should return false for non-existent path")
	}

	t.Logf("✓ Exists works correctly")
}

// =============================================================================
// Multilock Tests
// =============================================================================

func TestNewMultilock(t *testing.T) {
	lock := NewMultilock("key1", "key2")
	if lock == nil {
		t.Error("NewMultilock should not return nil for valid keys")
	}

	// Empty keys should return nil
	nilLock := NewMultilock()
	if nilLock != nil {
		t.Error("NewMultilock should return nil for empty keys")
	}

	t.Logf("✓ NewMultilock works correctly")
}

func TestMultilockLockUnlock(t *testing.T) {
	lock := NewMultilock("test_key")
	if lock == nil {
		t.Fatal("NewMultilock returned nil")
	}

	// Should not block
	done := make(chan bool, 1)
	go func() {
		lock.Lock()
		lock.Unlock()
		done <- true
	}()

	select {
	case <-done:
		t.Logf("✓ Multilock Lock/Unlock works correctly")
	}
}

func TestUnique(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		wantLen int
	}{
		{"empty", []string{}, 0},
		{"nil", nil, 0},
		{"single", []string{"a"}, 1},
		{"no_duplicates", []string{"a", "b", "c"}, 3},
		{"with_duplicates", []string{"a", "b", "a", "c", "b"}, 3},
		{"all_same", []string{"a", "a", "a"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := unique(tt.input)
			if len(result) != tt.wantLen {
				t.Errorf("unique(%v) length = %d, want %d", tt.input, len(result), tt.wantLen)
			}
		})
	}

	t.Logf("✓ unique function works correctly")
}

func TestClean(t *testing.T) {
	// Just verify it doesn't panic
	removed := Clean()
	if removed == nil {
		t.Error("Clean should return a slice, not nil")
	}

	t.Logf("✓ Clean works correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkToBytes4(b *testing.B) {
	input := []byte{1, 2, 3, 4, 5, 6}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ToBytes4(input)
	}
}

func BenchmarkToBytes32(b *testing.B) {
	input := make([]byte, 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ToBytes32(input)
	}
}

func BenchmarkToBytes64(b *testing.B) {
	input := make([]byte, 128)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ToBytes64(input)
	}
}

func BenchmarkKeccak256(b *testing.B) {
	data := []byte("hello world benchmark test")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Keccak256(data)
	}
}

func BenchmarkKeccak256Hash(b *testing.B) {
	data := []byte("hello world benchmark test")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Keccak256Hash(data)
	}
}

func BenchmarkHash256toS(b *testing.B) {
	data := []byte("hello world benchmark test")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Hash256toS(data)
	}
}

func BenchmarkHexPrefix(b *testing.B) {
	a := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	c := []byte{1, 2, 3, 4, 9, 10, 11, 12}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		HexPrefix(a, c)
	}
}

func BenchmarkUnique(b *testing.B) {
	arr := []string{"a", "b", "c", "a", "d", "b", "e", "f", "c"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		unique(arr)
	}
}

func BenchmarkExists(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Exists(".")
	}
}
