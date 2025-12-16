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

// =============================================================================
// Response Code Tests
// =============================================================================

func TestResponseCodes(t *testing.T) {
	// Verify response codes
	if responseCodeSuccess != 0x00 {
		t.Errorf("responseCodeSuccess should be 0x00, got %x", responseCodeSuccess)
	}
	if responseCodeInvalidRequest != 0x01 {
		t.Errorf("responseCodeInvalidRequest should be 0x01, got %x", responseCodeInvalidRequest)
	}
	if responseCodeServerError != 0x02 {
		t.Errorf("responseCodeServerError should be 0x02, got %x", responseCodeServerError)
	}

	t.Logf("✓ Response codes are correct")
}

func TestResponseCodeUniqueness(t *testing.T) {
	codes := []byte{responseCodeSuccess, responseCodeInvalidRequest, responseCodeServerError}
	seen := make(map[byte]bool)

	for _, code := range codes {
		if seen[code] {
			t.Errorf("Duplicate response code: %x", code)
		}
		seen[code] = true
	}

	t.Logf("✓ Response codes are unique")
}

// =============================================================================
// Stream Error Validation Tests
// =============================================================================

func TestIsValidStreamErrorNil(t *testing.T) {
	result := isValidStreamError(nil)
	if result {
		t.Error("isValidStreamError(nil) should return false")
	}

	t.Logf("✓ isValidStreamError handles nil correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkResponseCodeCheck(b *testing.B) {
	code := responseCodeSuccess

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = code == responseCodeSuccess
	}
}

