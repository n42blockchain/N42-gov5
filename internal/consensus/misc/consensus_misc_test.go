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

package misc

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Constants Tests
// =============================================================================

func TestPoAConstantsValues(t *testing.T) {
	// Verify epoch length
	if DefaultEpochLength != 30000 {
		t.Errorf("DefaultEpochLength should be 30000, got %d", DefaultEpochLength)
	}

	// Verify extra data sizes
	if ExtraVanity != 32 {
		t.Errorf("ExtraVanity should be 32, got %d", ExtraVanity)
	}
	if ExtraSeal != 65 { // crypto.SignatureLength
		t.Errorf("ExtraSeal should be 65, got %d", ExtraSeal)
	}

	// Verify cache sizes
	if InmemorySnapshots != 128 {
		t.Errorf("InmemorySnapshots should be 128, got %d", InmemorySnapshots)
	}
	if InmemorySignatures != 4096 {
		t.Errorf("InmemorySignatures should be 4096, got %d", InmemorySignatures)
	}

	// Verify wiggle time
	if WiggleTime != 500*time.Millisecond {
		t.Errorf("WiggleTime should be 500ms, got %v", WiggleTime)
	}

	t.Logf("✓ PoA constants are correct")
}

func TestNonceVotesValues(t *testing.T) {
	// Verify NonceAuthVote
	if len(NonceAuthVote) != 8 {
		t.Errorf("NonceAuthVote should be 8 bytes, got %d", len(NonceAuthVote))
	}
	for _, b := range NonceAuthVote {
		if b != 0xff {
			t.Error("NonceAuthVote should be all 0xff")
			break
		}
	}

	// Verify NonceDropVote
	if len(NonceDropVote) != 8 {
		t.Errorf("NonceDropVote should be 8 bytes, got %d", len(NonceDropVote))
	}
	for _, b := range NonceDropVote {
		if b != 0x00 {
			t.Error("NonceDropVote should be all 0x00")
			break
		}
	}

	t.Logf("✓ Nonce votes are correct")
}

func TestDifficultyConstantsValues(t *testing.T) {
	// DiffInTurn should be 2
	if DiffInTurn.Uint64() != 2 {
		t.Errorf("DiffInTurn should be 2, got %d", DiffInTurn.Uint64())
	}

	// DiffNoTurn should be 1
	if DiffNoTurn.Uint64() != 1 {
		t.Errorf("DiffNoTurn should be 1, got %d", DiffNoTurn.Uint64())
	}

	t.Logf("✓ Difficulty constants are correct")
}

// =============================================================================
// Gas Limit Tests
// =============================================================================

func TestVerifyGaslimitValues(t *testing.T) {
	parentGasLimit := uint64(8000000)

	tests := []struct {
		name           string
		parentGasLimit uint64
		headerGasLimit uint64
		expectErr      bool
	}{
		{"same", parentGasLimit, parentGasLimit, false},
		{"slight_increase", parentGasLimit, parentGasLimit + 100, false},
		{"slight_decrease", parentGasLimit, parentGasLimit - 100, false},
		{"max_increase", parentGasLimit, parentGasLimit + parentGasLimit/params.GasLimitBoundDivisor - 1, false},
		{"max_decrease", parentGasLimit, parentGasLimit - parentGasLimit/params.GasLimitBoundDivisor + 1, false},
		{"too_high", parentGasLimit, parentGasLimit + parentGasLimit/params.GasLimitBoundDivisor + 1, true},
		{"too_low", parentGasLimit, parentGasLimit - parentGasLimit/params.GasLimitBoundDivisor - 1, true},
		{"below_minimum", 10000, params.MinGasLimit - 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyGaslimit(tt.parentGasLimit, tt.headerGasLimit)
			if tt.expectErr && err == nil {
				t.Errorf("VerifyGaslimit(%d, %d) should return error", tt.parentGasLimit, tt.headerGasLimit)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("VerifyGaslimit(%d, %d) should not return error: %v", tt.parentGasLimit, tt.headerGasLimit, err)
			}
		})
	}

	t.Logf("✓ VerifyGaslimit works correctly")
}

// =============================================================================
// Error Tests
// =============================================================================

func TestErrorTypesExist(t *testing.T) {
	// Verify core error types exist and are non-nil
	errors := []error{
		ErrInvalidDifficulty,
		ErrWrongDifficulty,
		ErrInvalidTimestamp,
		ErrInvalidMixDigest,
		ErrInvalidUncleHash,
	}

	for i, err := range errors {
		if err == nil {
			t.Errorf("Error %d should not be nil", i)
		}
	}

	t.Logf("✓ Error types are correctly defined")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkVerifyGaslimitCheck(b *testing.B) {
	parentGasLimit := uint64(8000000)
	headerGasLimit := uint64(8000100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		VerifyGaslimit(parentGasLimit, headerGasLimit)
	}
}
