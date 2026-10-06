// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for HeaderValidator (header.go): ValidateBasicFields,
// ValidateTimestamp, ValidateGasUsed, ValidateMixDigest,
// ValidateCheckpointSigners, ExtractSignersFromCheckpoint, PrepareExtraData.

package misc

import (
	"bytes"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func g35ValidHeader(number uint64) *block.Header {
	extra := make([]byte, ExtraVanity+ExtraSeal)
	return &block.Header{
		Number:     uint256.NewInt(number),
		Time:       1,
		Nonce:      block.BlockNonce{0, 0, 0, 0, 0, 0, 0, 0},
		Extra:      extra,
		Difficulty: uint256.NewInt(1),
		GasLimit:   params.MinGasLimit,
	}
}

func TestG35ValidateBasicFieldsHappyPath(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(1)
	if err := v.ValidateBasicFields(h); err != nil {
		t.Fatalf("expected valid header, got %v", err)
	}
}

func TestG35ValidateBasicFieldsGenesisRejected(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(0)
	if err := v.ValidateBasicFields(h); err != ErrUnknownBlock {
		t.Fatalf("expected ErrUnknownBlock for genesis, got %v", err)
	}
}

func TestG35ValidateBasicFieldsFutureBlock(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(1)
	h.Time = uint64(time.Now().Add(time.Hour).Unix())
	if err := v.ValidateBasicFields(h); err != ErrFutureBlock {
		t.Fatalf("expected ErrFutureBlock, got %v", err)
	}
}

func TestG35ValidateBasicFieldsCheckpointBeneficiary(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(10) // checkpoint: 10 % 10 == 0
	h.Coinbase = types.Address{1}
	if err := v.ValidateBasicFields(h); err != ErrInvalidCheckpointBeneficiary {
		t.Fatalf("expected ErrInvalidCheckpointBeneficiary, got %v", err)
	}
}

func TestG35ValidateBasicFieldsInvalidVote(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(1)
	h.Nonce = block.BlockNonce{1, 2, 3, 4, 5, 6, 7, 8}
	if err := v.ValidateBasicFields(h); err != ErrInvalidVote {
		t.Fatalf("expected ErrInvalidVote, got %v", err)
	}
}

func TestG35ValidateBasicFieldsCheckpointVote(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(10)
	copy(h.Nonce[:], NonceAuthVote) // must be drop-vote on checkpoint
	if err := v.ValidateBasicFields(h); err != ErrInvalidCheckpointVote {
		t.Fatalf("expected ErrInvalidCheckpointVote, got %v", err)
	}
}

func TestG35ValidateBasicFieldsMissingVanityAndSignature(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(1)
	h.Extra = make([]byte, 10)
	if err := v.ValidateBasicFields(h); err != ErrMissingVanity {
		t.Fatalf("expected ErrMissingVanity, got %v", err)
	}

	h2 := g35ValidHeader(1)
	h2.Extra = make([]byte, ExtraVanity)
	if err := v.ValidateBasicFields(h2); err != ErrMissingSignature {
		t.Fatalf("expected ErrMissingSignature, got %v", err)
	}
}

func TestG35ValidateBasicFieldsExtraSigners(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(1) // not a checkpoint
	h.Extra = make([]byte, ExtraVanity+ExtraSeal+types.AddressLength)
	if err := v.ValidateBasicFields(h); err != ErrExtraSigners {
		t.Fatalf("expected ErrExtraSigners, got %v", err)
	}
}

func TestG35ValidateBasicFieldsInvalidCheckpointSigners(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(10)
	h.Extra = make([]byte, ExtraVanity+ExtraSeal+types.AddressLength+1) // not a multiple of 20
	copy(h.Nonce[:], NonceDropVote)
	if err := v.ValidateBasicFields(h); err != ErrInvalidCheckpointSigners {
		t.Fatalf("expected ErrInvalidCheckpointSigners, got %v", err)
	}
}

func TestG35ValidateBasicFieldsGasLimitTooHigh(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(1)
	h.GasLimit = params.MaxGasLimit + 1
	if err := v.ValidateBasicFields(h); err == nil {
		t.Fatalf("expected error for gas limit exceeding max")
	}
}

func TestG35ValidateBasicFieldsNilNumber(t *testing.T) {
	v := NewHeaderValidator(10)
	h := g35ValidHeader(1)
	h.Number = nil
	if err := v.ValidateBasicFields(h); err == nil {
		t.Fatalf("expected error for nil header number")
	}
}

func TestG35ValidateTimestamp(t *testing.T) {
	v := NewHeaderValidator(10)
	parent := &block.Header{Time: 100}
	header := &block.Header{Time: 115}
	if err := v.ValidateTimestamp(header, parent, 10); err != nil {
		t.Fatalf("expected valid timestamp, got %v", err)
	}
	header2 := &block.Header{Time: 105}
	if err := v.ValidateTimestamp(header2, parent, 10); err != ErrInvalidTimestamp {
		t.Fatalf("expected ErrInvalidTimestamp, got %v", err)
	}
}

func TestG35ValidateGasUsed(t *testing.T) {
	v := NewHeaderValidator(10)
	h := &block.Header{GasUsed: 100, GasLimit: 200}
	if err := v.ValidateGasUsed(h); err != nil {
		t.Fatalf("expected valid gas used, got %v", err)
	}
	h2 := &block.Header{GasUsed: 300, GasLimit: 200}
	if err := v.ValidateGasUsed(h2); err == nil {
		t.Fatalf("expected error for gasUsed > gasLimit")
	}
}

func TestG35ValidateMixDigest(t *testing.T) {
	v := NewHeaderValidator(10)
	h := &block.Header{}
	if err := v.ValidateMixDigest(h); err != nil {
		t.Fatalf("expected valid zero mix digest, got %v", err)
	}
	h2 := &block.Header{MixDigest: types.Hash{1}}
	if err := v.ValidateMixDigest(h2); err != ErrInvalidMixDigest {
		t.Fatalf("expected ErrInvalidMixDigest, got %v", err)
	}
}

func TestG35ValidateCheckpointSignersAndExtract(t *testing.T) {
	v := NewHeaderValidator(10)
	signers := []types.Address{{1}, {2}}
	extra := PrepareExtraData(nil, signers, true)
	h := &block.Header{Number: uint256.NewInt(10), Extra: extra}

	if err := v.ValidateCheckpointSigners(h, signers); err != nil {
		t.Fatalf("expected matching signers, got %v", err)
	}

	wrongSigners := []types.Address{{9}, {9}}
	if err := v.ValidateCheckpointSigners(h, wrongSigners); err != ErrMismatchingCheckpointSigners {
		t.Fatalf("expected mismatch error, got %v", err)
	}

	// non-checkpoint block: always nil.
	h2 := &block.Header{Number: uint256.NewInt(1), Extra: extra}
	if err := v.ValidateCheckpointSigners(h2, signers); err != nil {
		t.Fatalf("expected nil for non-checkpoint block, got %v", err)
	}

	got, err := ExtractSignersFromCheckpoint(h)
	if err != nil {
		t.Fatalf("ExtractSignersFromCheckpoint: %v", err)
	}
	if len(got) != 2 || got[0] != signers[0] || got[1] != signers[1] {
		t.Fatalf("expected %v, got %v", signers, got)
	}

	if _, err := ExtractSignersFromCheckpoint(&block.Header{Extra: make([]byte, 5)}); err != ErrMissingSignature {
		t.Fatalf("expected ErrMissingSignature for short extra, got %v", err)
	}

	badExtra := make([]byte, ExtraVanity+ExtraSeal+1)
	if _, err := ExtractSignersFromCheckpoint(&block.Header{Extra: badExtra}); err != ErrInvalidCheckpointSigners {
		t.Fatalf("expected ErrInvalidCheckpointSigners, got %v", err)
	}
}

func TestG35PrepareExtraDataPadsShortVanity(t *testing.T) {
	out := PrepareExtraData([]byte{1, 2}, nil, false)
	if len(out) != ExtraVanity+ExtraSeal {
		t.Fatalf("expected padded vanity + seal length, got %d", len(out))
	}
	if !bytes.Equal(out[:2], []byte{1, 2}) {
		t.Fatalf("expected original prefix preserved")
	}
}

func TestG35IsCheckpointAndEpoch(t *testing.T) {
	v := NewHeaderValidator(10)
	if v.Epoch() != 10 {
		t.Fatalf("expected epoch 10")
	}
	if !v.IsCheckpoint(20) || v.IsCheckpoint(21) {
		t.Fatalf("unexpected checkpoint result")
	}

	v2 := NewHeaderValidator(0)
	if v2.Epoch() != DefaultEpochLength {
		t.Fatalf("expected default epoch when 0 passed")
	}
}
