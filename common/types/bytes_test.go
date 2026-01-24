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

package types

import (
	"bytes"
	"testing"
)

func TestFromHex1(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect []byte
	}{
		{"with 0x prefix", "0xabcd", []byte{0xab, 0xcd}},
		{"with 0X prefix", "0Xabcd", []byte{0xab, 0xcd}},
		{"without prefix", "abcd", []byte{0xab, 0xcd}},
		{"odd length", "abc", []byte{0x0a, 0xbc}},
		{"empty", "", []byte{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FromHex1(tt.input)
			if !bytes.Equal(result, tt.expect) {
				t.Errorf("FromHex1() = %v, want %v", result, tt.expect)
			}
		})
	}
}

func TestHas0xPrefix(t *testing.T) {
	tests := []struct {
		input  string
		expect bool
	}{
		{"0xabcd", true},
		{"0Xabcd", true},
		{"abcd", false},
		{"0", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := has0xPrefix(tt.input)
			if result != tt.expect {
				t.Errorf("has0xPrefix(%q) = %v, want %v", tt.input, result, tt.expect)
			}
		})
	}
}

func TestHex2Bytes(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect []byte
	}{
		{"valid hex", "abcd", []byte{0xab, 0xcd}},
		{"empty", "", []byte{}},
		{"invalid hex", "xyz", []byte{}}, // Hex2Bytes ignores errors
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Hex2Bytes(tt.input)
			if !bytes.Equal(result, tt.expect) {
				t.Errorf("Hex2Bytes() = %v, want %v", result, tt.expect)
			}
		})
	}
}

func TestKeyCmp(t *testing.T) {
	tests := []struct {
		name       string
		key1       []byte
		key2       []byte
		expectCmp  int
		expectBoth bool
	}{
		{"both empty", []byte{}, []byte{}, 0, true},
		{"key1 empty", []byte{}, []byte{1}, 1, false},
		{"key2 empty", []byte{1}, []byte{}, -1, false},
		{"key1 < key2", []byte{1}, []byte{2}, -1, false},
		{"key1 > key2", []byte{2}, []byte{1}, 1, false},
		{"key1 == key2", []byte{1, 2}, []byte{1, 2}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmp, bothEmpty := KeyCmp(tt.key1, tt.key2)
			if cmp != tt.expectCmp {
				t.Errorf("KeyCmp() cmp = %d, want %d", cmp, tt.expectCmp)
			}
			if bothEmpty != tt.expectBoth {
				t.Errorf("KeyCmp() bothEmpty = %v, want %v", bothEmpty, tt.expectBoth)
			}
		})
	}
}

func TestCopyBytes(t *testing.T) {
	// Test with nil
	result := CopyBytes(nil)
	if result != nil {
		t.Error("CopyBytes(nil) should return nil")
	}

	// Test with data
	original := []byte{1, 2, 3, 4, 5}
	copied := CopyBytes(original)

	if !bytes.Equal(copied, original) {
		t.Error("CopyBytes() didn't copy data correctly")
	}

	// Verify it's a copy, not the same slice
	copied[0] = 99
	if original[0] == 99 {
		t.Error("CopyBytes() returned same slice, not a copy")
	}
}

func TestRightPadBytes(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		length int
		expect []byte
	}{
		{"pad needed", []byte{1, 2}, 5, []byte{1, 2, 0, 0, 0}},
		{"no pad needed", []byte{1, 2, 3}, 3, []byte{1, 2, 3}},
		{"length smaller", []byte{1, 2, 3, 4}, 2, []byte{1, 2, 3, 4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RightPadBytes(tt.input, tt.length)
			if !bytes.Equal(result, tt.expect) {
				t.Errorf("RightPadBytes() = %v, want %v", result, tt.expect)
			}
		})
	}
}

func TestLeftPadBytes(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		length int
		expect []byte
	}{
		{"pad needed", []byte{1, 2}, 5, []byte{0, 0, 0, 1, 2}},
		{"no pad needed", []byte{1, 2, 3}, 3, []byte{1, 2, 3}},
		{"length smaller", []byte{1, 2, 3, 4}, 2, []byte{1, 2, 3, 4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := LeftPadBytes(tt.input, tt.length)
			if !bytes.Equal(result, tt.expect) {
				t.Errorf("LeftPadBytes() = %v, want %v", result, tt.expect)
			}
		})
	}
}

func TestTrimLeftZeroes(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect []byte
	}{
		{"with leading zeroes", []byte{0, 0, 1, 2, 3}, []byte{1, 2, 3}},
		{"no leading zeroes", []byte{1, 2, 3}, []byte{1, 2, 3}},
		{"all zeroes", []byte{0, 0, 0}, []byte{}},
		{"empty", []byte{}, []byte{}},
		{"single non-zero", []byte{0, 0, 5}, []byte{5}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TrimLeftZeroes(tt.input)
			if !bytes.Equal(result, tt.expect) {
				t.Errorf("TrimLeftZeroes() = %v, want %v", result, tt.expect)
			}
		})
	}
}

func TestTrimRightZeroes(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect []byte
	}{
		{"with trailing zeroes", []byte{1, 2, 3, 0, 0}, []byte{1, 2, 3}},
		{"no trailing zeroes", []byte{1, 2, 3}, []byte{1, 2, 3}},
		{"all zeroes", []byte{0, 0, 0}, []byte{}},
		{"empty", []byte{}, []byte{}},
		{"single non-zero", []byte{5, 0, 0}, []byte{5}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TrimRightZeroes(tt.input)
			if !bytes.Equal(result, tt.expect) {
				t.Errorf("TrimRightZeroes() = %v, want %v", result, tt.expect)
			}
		})
	}
}
