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
	"bytes"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Constants Tests
// =============================================================================

func TestConstants(t *testing.T) {
	t.Run("DefaultEpochLength", func(t *testing.T) {
		if DefaultEpochLength != 30000 {
			t.Errorf("DefaultEpochLength = %d, want 30000", DefaultEpochLength)
		}
	})

	t.Run("ExtraVanity", func(t *testing.T) {
		if ExtraVanity != 32 {
			t.Errorf("ExtraVanity = %d, want 32", ExtraVanity)
		}
	})

	t.Run("ExtraSeal", func(t *testing.T) {
		if ExtraSeal != 65 { // crypto.SignatureLength
			t.Errorf("ExtraSeal = %d, want 65", ExtraSeal)
		}
	})

	t.Run("InmemorySnapshots", func(t *testing.T) {
		if InmemorySnapshots != 128 {
			t.Errorf("InmemorySnapshots = %d, want 128", InmemorySnapshots)
		}
	})

	t.Run("InmemorySignatures", func(t *testing.T) {
		if InmemorySignatures != 4096 {
			t.Errorf("InmemorySignatures = %d, want 4096", InmemorySignatures)
		}
	})

	t.Run("WiggleTime", func(t *testing.T) {
		if WiggleTime != 500*time.Millisecond {
			t.Errorf("WiggleTime = %v, want 500ms", WiggleTime)
		}
	})

	t.Run("NonceAuthVote", func(t *testing.T) {
		expected := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
		if !bytes.Equal(NonceAuthVote, expected) {
			t.Errorf("NonceAuthVote = %x, want %x", NonceAuthVote, expected)
		}
	})

	t.Run("NonceDropVote", func(t *testing.T) {
		expected := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		if !bytes.Equal(NonceDropVote, expected) {
			t.Errorf("NonceDropVote = %x, want %x", NonceDropVote, expected)
		}
	})

	t.Run("DiffInTurn", func(t *testing.T) {
		if DiffInTurn.Cmp(uint256.NewInt(2)) != 0 {
			t.Errorf("DiffInTurn = %s, want 2", DiffInTurn.String())
		}
	})

	t.Run("DiffNoTurn", func(t *testing.T) {
		if DiffNoTurn.Cmp(uint256.NewInt(1)) != 0 {
			t.Errorf("DiffNoTurn = %s, want 1", DiffNoTurn.String())
		}
	})
}

// =============================================================================
// Difficulty Tests
// =============================================================================

type mockInturn struct {
	inTurn bool
}

func (m *mockInturn) Inturn(blockNumber uint64, signer types.Address) bool {
	return m.inTurn
}

func TestCalcDifficulty(t *testing.T) {
	signer := types.Address{1, 2, 3}

	t.Run("InTurn", func(t *testing.T) {
		mock := &mockInturn{inTurn: true}
		diff := CalcDifficulty(mock, 100, signer)
		if diff.Cmp(DiffInTurn) != 0 {
			t.Errorf("CalcDifficulty(in-turn) = %s, want %s", diff.String(), DiffInTurn.String())
		}
	})

	t.Run("NotInTurn", func(t *testing.T) {
		mock := &mockInturn{inTurn: false}
		diff := CalcDifficulty(mock, 100, signer)
		if diff.Cmp(DiffNoTurn) != 0 {
			t.Errorf("CalcDifficulty(not-in-turn) = %s, want %s", diff.String(), DiffNoTurn.String())
		}
	})
}

func TestValidateDifficulty(t *testing.T) {
	tests := []struct {
		name    string
		diff    *uint256.Int
		wantErr bool
	}{
		{"Zero", uint256.NewInt(0), true},
		{"One (NoTurn)", uint256.NewInt(1), false},
		{"Two (InTurn)", uint256.NewInt(2), false},
		{"Three (Invalid)", uint256.NewInt(3), true},
		{"Large", uint256.NewInt(100), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDifficulty(tt.diff)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDifficulty(%s) error = %v, wantErr %v", tt.diff.String(), err, tt.wantErr)
			}
		})
	}
}

func TestVerifyDifficulty(t *testing.T) {
	tests := []struct {
		name    string
		diff    *uint256.Int
		inturn  bool
		wantErr bool
	}{
		{"InTurn correct", uint256.NewInt(2), true, false},
		{"InTurn wrong", uint256.NewInt(1), true, true},
		{"NoTurn correct", uint256.NewInt(1), false, false},
		{"NoTurn wrong", uint256.NewInt(2), false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyDifficulty(tt.diff, tt.inturn)
			if (err != nil) != tt.wantErr {
				t.Errorf("VerifyDifficulty(%s, %v) error = %v, wantErr %v", tt.diff.String(), tt.inturn, err, tt.wantErr)
			}
		})
	}
}

// =============================================================================
// Header Validator Tests
// =============================================================================

func TestNewHeaderValidator(t *testing.T) {
	t.Run("DefaultEpoch", func(t *testing.T) {
		v := NewHeaderValidator(0)
		if v.Epoch() != DefaultEpochLength {
			t.Errorf("Epoch() = %d, want %d", v.Epoch(), DefaultEpochLength)
		}
	})

	t.Run("CustomEpoch", func(t *testing.T) {
		v := NewHeaderValidator(1000)
		if v.Epoch() != 1000 {
			t.Errorf("Epoch() = %d, want 1000", v.Epoch())
		}
	})
}

func TestIsCheckpoint(t *testing.T) {
	v := NewHeaderValidator(1000)

	tests := []struct {
		number     uint64
		isCheckpoint bool
	}{
		{0, true},
		{1, false},
		{999, false},
		{1000, true},
		{1001, false},
		{2000, true},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			if got := v.IsCheckpoint(tt.number); got != tt.isCheckpoint {
				t.Errorf("IsCheckpoint(%d) = %v, want %v", tt.number, got, tt.isCheckpoint)
			}
		})
	}
}

// =============================================================================
// Seal Tests
// =============================================================================

func TestNewSignatureCache(t *testing.T) {
	cache := NewSignatureCache()
	if cache == nil {
		t.Error("NewSignatureCache() returned nil")
	}
}

func TestNewSnapshotCache(t *testing.T) {
	cache := NewSnapshotCache()
	if cache == nil {
		t.Error("NewSnapshotCache() returned nil")
	}
}

// =============================================================================
// Header Extra Data Tests
// =============================================================================

func TestPrepareExtraData(t *testing.T) {
	signers := []types.Address{
		{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		{21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40},
	}

	t.Run("NonCheckpoint", func(t *testing.T) {
		extra := PrepareExtraData(nil, signers, false)
		expectedLen := ExtraVanity + ExtraSeal
		if len(extra) != expectedLen {
			t.Errorf("PrepareExtraData len = %d, want %d", len(extra), expectedLen)
		}
	})

	t.Run("Checkpoint", func(t *testing.T) {
		extra := PrepareExtraData(nil, signers, true)
		expectedLen := ExtraVanity + len(signers)*types.AddressLength + ExtraSeal
		if len(extra) != expectedLen {
			t.Errorf("PrepareExtraData len = %d, want %d", len(extra), expectedLen)
		}

		// Check signers are encoded correctly
		for i, signer := range signers {
			start := ExtraVanity + i*types.AddressLength
			end := start + types.AddressLength
			if !bytes.Equal(extra[start:end], signer[:]) {
				t.Errorf("Signer %d not encoded correctly", i)
			}
		}
	})

	t.Run("PreservesVanity", func(t *testing.T) {
		vanity := bytes.Repeat([]byte{0xab}, ExtraVanity)
		extra := PrepareExtraData(vanity, nil, false)
		if !bytes.Equal(extra[:ExtraVanity], vanity) {
			t.Error("Vanity not preserved")
		}
	})

	t.Run("PadsShortVanity", func(t *testing.T) {
		short := []byte{0x01, 0x02, 0x03}
		extra := PrepareExtraData(short, nil, false)
		if len(extra) != ExtraVanity+ExtraSeal {
			t.Errorf("len = %d, want %d", len(extra), ExtraVanity+ExtraSeal)
		}
		// First 3 bytes should be preserved
		if !bytes.Equal(extra[:3], short) {
			t.Error("Short vanity not preserved")
		}
	})
}

func TestExtractSignersFromCheckpoint(t *testing.T) {
	signers := []types.Address{
		{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		{21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40},
	}

	t.Run("ValidCheckpoint", func(t *testing.T) {
		extra := PrepareExtraData(nil, signers, true)
		header := &block.Header{Extra: extra}

		extracted, err := ExtractSignersFromCheckpoint(header)
		if err != nil {
			t.Fatalf("ExtractSignersFromCheckpoint error = %v", err)
		}

		if len(extracted) != len(signers) {
			t.Fatalf("len(extracted) = %d, want %d", len(extracted), len(signers))
		}

		for i, signer := range extracted {
			if signer != signers[i] {
				t.Errorf("Signer %d = %v, want %v", i, signer, signers[i])
			}
		}
	})

	t.Run("TooShort", func(t *testing.T) {
		header := &block.Header{Extra: make([]byte, ExtraVanity)}

		_, err := ExtractSignersFromCheckpoint(header)
		if err != ErrMissingSignature {
			t.Errorf("Error = %v, want ErrMissingSignature", err)
		}
	})

	t.Run("InvalidSignerLen", func(t *testing.T) {
		// Create extra with invalid signer length (not divisible by 20)
		extra := make([]byte, ExtraVanity+5+ExtraSeal)
		header := &block.Header{Extra: extra}

		_, err := ExtractSignersFromCheckpoint(header)
		if err != ErrInvalidCheckpointSigners {
			t.Errorf("Error = %v, want ErrInvalidCheckpointSigners", err)
		}
	})
}

// =============================================================================
// Error Tests
// =============================================================================

func TestErrors(t *testing.T) {
	// Ensure all errors are properly defined and have unique messages
	errors := map[string]error{
		"ErrUnknownBlock":                 ErrUnknownBlock,
		"ErrInvalidCheckpointBeneficiary": ErrInvalidCheckpointBeneficiary,
		"ErrInvalidVote":                  ErrInvalidVote,
		"ErrInvalidCheckpointVote":        ErrInvalidCheckpointVote,
		"ErrMissingVanity":                ErrMissingVanity,
		"ErrMissingSignature":             ErrMissingSignature,
		"ErrExtraSigners":                 ErrExtraSigners,
		"ErrInvalidCheckpointSigners":     ErrInvalidCheckpointSigners,
		"ErrMismatchingCheckpointSigners": ErrMismatchingCheckpointSigners,
		"ErrInvalidMixDigest":             ErrInvalidMixDigest,
		"ErrInvalidUncleHash":             ErrInvalidUncleHash,
		"ErrInvalidDifficulty":            ErrInvalidDifficulty,
		"ErrWrongDifficulty":              ErrWrongDifficulty,
		"ErrInvalidTimestamp":             ErrInvalidTimestamp,
		"ErrInvalidVotingChain":           ErrInvalidVotingChain,
		"ErrUnauthorizedSigner":           ErrUnauthorizedSigner,
		"ErrRecentlySigned":               ErrRecentlySigned,
		"ErrFutureBlock":                  ErrFutureBlock,
		"ErrInvalidGasLimit":              ErrInvalidGasLimit,
		"ErrInvalidGasUsed":               ErrInvalidGasUsed,
		"ErrUnknownAncestor":              ErrUnknownAncestor,
	}

	for name, err := range errors {
		if err == nil {
			t.Errorf("%s is nil", name)
		}
		if err.Error() == "" {
			t.Errorf("%s has empty message", name)
		}
	}
}

// =============================================================================
// Golden Sample Tests (for header verification consistency)
// =============================================================================

func TestGoldenSampleDifficulty(t *testing.T) {
	// Test that difficulty calculation produces consistent results
	signer := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create mock snapshots for in-turn and not-in-turn scenarios
	inTurn := &mockInturn{inTurn: true}
	notInTurn := &mockInturn{inTurn: false}

	// Test multiple block numbers
	for _, blockNum := range []uint64{1, 100, 1000, 30000, 100000} {
		inTurnDiff := CalcDifficulty(inTurn, blockNum, signer)
		notInTurnDiff := CalcDifficulty(notInTurn, blockNum, signer)

		if inTurnDiff.Cmp(uint256.NewInt(2)) != 0 {
			t.Errorf("Block %d: in-turn difficulty = %s, want 2", blockNum, inTurnDiff.String())
		}
		if notInTurnDiff.Cmp(uint256.NewInt(1)) != 0 {
			t.Errorf("Block %d: not-in-turn difficulty = %s, want 1", blockNum, notInTurnDiff.String())
		}
	}
}

