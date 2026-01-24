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
	"strings"
	"testing"
)

func TestStorageSizeString(t *testing.T) {
	tests := []struct {
		name     string
		size     StorageSize
		contains string
	}{
		{"bytes", StorageSize(500), "B"},
		{"kilobytes", StorageSize(1500), "KiB"},
		{"megabytes", StorageSize(1500000), "MiB"},
		{"gigabytes", StorageSize(1500000000), "GiB"},
		{"terabytes", StorageSize(1500000000000), "TiB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.size.String()
			if !strings.Contains(result, tt.contains) {
				t.Errorf("StorageSize.String() = %s, want to contain %s", result, tt.contains)
			}
		})
	}
}

func TestStorageSizeTerminalString(t *testing.T) {
	tests := []struct {
		name     string
		size     StorageSize
		contains string
	}{
		{"bytes", StorageSize(500), "B"},
		{"kilobytes", StorageSize(1500), "KiB"},
		{"megabytes", StorageSize(1500000), "MiB"},
		{"gigabytes", StorageSize(1500000000), "GiB"},
		{"terabytes", StorageSize(1500000000000), "TiB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.size.TerminalString()
			if !strings.Contains(result, tt.contains) {
				t.Errorf("StorageSize.TerminalString() = %s, want to contain %s", result, tt.contains)
			}
			// TerminalString should not have space before unit
			if strings.Contains(result, " ") {
				t.Errorf("StorageSize.TerminalString() = %s, should not contain space", result)
			}
		})
	}
}

func TestStorageSizeBoundaries(t *testing.T) {
	// Test exact boundary values
	tests := []struct {
		name     string
		size     StorageSize
		contains string
	}{
		{"exactly 1 KiB", StorageSize(1024), "B"},       // 1024 is not > 1024, so still bytes
		{"just over 1 KiB", StorageSize(1025), "KiB"},   // > 1024 = KiB
		{"exactly 1 MiB", StorageSize(1048576), "KiB"},  // 1048576 is not > 1048576
		{"just over 1 MiB", StorageSize(1048577), "MiB"},
		{"exactly 1 GiB", StorageSize(1073741824), "MiB"},
		{"just over 1 GiB", StorageSize(1073741825), "GiB"},
		{"exactly 1 TiB", StorageSize(1099511627776), "GiB"},
		{"just over 1 TiB", StorageSize(1099511627777), "TiB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.size.String()
			if !strings.Contains(result, tt.contains) {
				t.Errorf("StorageSize(%v).String() = %s, want to contain %s", float64(tt.size), result, tt.contains)
			}
		})
	}
}
