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

package sync

import (
	"testing"
)

func TestResponseCodes(t *testing.T) {
	tests := []struct {
		name string
		code byte
		want byte
	}{
		{"success", responseCodeSuccess, 0x00},
		{"invalidRequest", responseCodeInvalidRequest, 0x01},
		{"serverError", responseCodeServerError, 0x02},
	}

	for _, tt := range tests {
		if tt.code != tt.want {
			t.Errorf("%s: got %#x, want %#x", tt.name, tt.code, tt.want)
		}
	}
}

func TestResponseCodeUniqueness(t *testing.T) {
	codes := []byte{responseCodeSuccess, responseCodeInvalidRequest, responseCodeServerError}
	seen := make(map[byte]bool, len(codes))

	for _, code := range codes {
		if seen[code] {
			t.Errorf("duplicate response code: %#x", code)
		}
		seen[code] = true
	}
}

func TestIsValidStreamErrorNil(t *testing.T) {
	if isValidStreamError(nil) {
		t.Error("isValidStreamError(nil) should return false")
	}
}
