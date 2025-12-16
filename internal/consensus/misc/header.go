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
	"fmt"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// HeaderValidator contains common header validation logic for PoA consensus engines.
type HeaderValidator struct {
	epoch uint64
}

// NewHeaderValidator creates a new HeaderValidator.
func NewHeaderValidator(epoch uint64) *HeaderValidator {
	if epoch == 0 {
		epoch = DefaultEpochLength
	}
	return &HeaderValidator{epoch: epoch}
}

// ValidateBasicFields performs basic validation on a header that doesn't require
// access to parent headers or snapshots.
func (v *HeaderValidator) ValidateBasicFields(header *block.Header) error {
	number := header.Number.Uint64()

	// Genesis block validation is not supported
	if header.Number.IsZero() {
		return ErrUnknownBlock
	}

	// Don't waste time checking blocks from the future
	if header.Time > uint64(time.Now().Unix()) {
		return ErrFutureBlock
	}

	// Checkpoint blocks need to enforce zero beneficiary
	checkpoint := (number % v.epoch) == 0
	if checkpoint && header.Coinbase != (types.Address{}) {
		return ErrInvalidCheckpointBeneficiary
	}

	// Nonces must be 0x00..0 or 0xff..f, zeroes enforced on checkpoints
	if !bytes.Equal(header.Nonce[:], NonceAuthVote) && !bytes.Equal(header.Nonce[:], NonceDropVote) {
		return ErrInvalidVote
	}
	if checkpoint && !bytes.Equal(header.Nonce[:], NonceDropVote) {
		return ErrInvalidCheckpointVote
	}

	// Check that the extra-data contains both the vanity and signature
	if len(header.Extra) < ExtraVanity {
		return ErrMissingVanity
	}
	if len(header.Extra) < ExtraVanity+ExtraSeal {
		return ErrMissingSignature
	}

	// Ensure that the extra-data contains a signer list on checkpoint, but none otherwise
	signersBytes := len(header.Extra) - ExtraVanity - ExtraSeal
	if !checkpoint && signersBytes != 0 {
		return ErrExtraSigners
	}
	if checkpoint && signersBytes%types.AddressLength != 0 {
		return ErrInvalidCheckpointSigners
	}

	// Ensure that the block's difficulty is meaningful (may not be correct at this point)
	if number > 0 {
		if err := ValidateDifficulty(header.Difficulty); err != nil {
			return err
		}
	}

	// Verify that the gas limit is <= 2^63-1
	if header.GasLimit > params.MaxGasLimit {
		return fmt.Errorf("invalid gasLimit: have %v, max %v", header.GasLimit, params.MaxGasLimit)
	}

	return nil
}

// ValidateTimestamp checks if the block's timestamp is valid compared to its parent.
func (v *HeaderValidator) ValidateTimestamp(header, parent *block.Header, period uint64) error {
	if parent.Time+period > header.Time {
		return ErrInvalidTimestamp
	}
	return nil
}

// ValidateGasUsed checks if gasUsed is <= gasLimit.
func (v *HeaderValidator) ValidateGasUsed(header *block.Header) error {
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("invalid gasUsed: have %d, gasLimit %d", header.GasUsed, header.GasLimit)
	}
	return nil
}

// ValidateMixDigest checks if the mix digest is zero (for APOA).
// Note: APOS uses MixDigest for BeforeStateRoot, so this is not always called.
func (v *HeaderValidator) ValidateMixDigest(header *block.Header) error {
	if header.MixDigest != (types.Hash{}) {
		return ErrInvalidMixDigest
	}
	return nil
}

// ValidateCheckpointSigners verifies that a checkpoint block contains the correct signer list.
func (v *HeaderValidator) ValidateCheckpointSigners(header *block.Header, expectedSigners []types.Address) error {
	number := header.Number.Uint64()
	if number%v.epoch != 0 {
		return nil // Not a checkpoint block
	}

	signers := make([]byte, len(expectedSigners)*types.AddressLength)
	for i, signer := range expectedSigners {
		copy(signers[i*types.AddressLength:], signer[:])
	}
	extraSuffix := len(header.Extra) - ExtraSeal
	if !bytes.Equal(header.Extra[ExtraVanity:extraSuffix], signers) {
		return ErrMismatchingCheckpointSigners
	}
	return nil
}

// IsCheckpoint returns true if the block number is a checkpoint block.
func (v *HeaderValidator) IsCheckpoint(number uint64) bool {
	return number%v.epoch == 0
}

// Epoch returns the epoch length.
func (v *HeaderValidator) Epoch() uint64 {
	return v.epoch
}

// ExtractSignersFromCheckpoint extracts the signer list from a checkpoint header.
func ExtractSignersFromCheckpoint(header *block.Header) ([]types.Address, error) {
	if len(header.Extra) < ExtraVanity+ExtraSeal {
		return nil, ErrMissingSignature
	}

	signersBytes := len(header.Extra) - ExtraVanity - ExtraSeal
	if signersBytes%types.AddressLength != 0 {
		return nil, ErrInvalidCheckpointSigners
	}

	signers := make([]types.Address, signersBytes/types.AddressLength)
	for i := 0; i < len(signers); i++ {
		copy(signers[i][:], header.Extra[ExtraVanity+i*types.AddressLength:])
	}
	return signers, nil
}

// PrepareExtraData prepares the extra-data field for a new header.
// It ensures the vanity prefix is present and adds the signer list for checkpoint blocks.
func PrepareExtraData(extra []byte, signers []types.Address, isCheckpoint bool) []byte {
	// Ensure the extra data has the vanity prefix
	if len(extra) < ExtraVanity {
		extra = append(extra, bytes.Repeat([]byte{0x00}, ExtraVanity-len(extra))...)
	}
	extra = extra[:ExtraVanity]

	// Add signer list for checkpoint blocks
	if isCheckpoint {
		for _, signer := range signers {
			extra = append(extra, signer[:]...)
		}
	}

	// Reserve space for the seal
	extra = append(extra, make([]byte, ExtraSeal)...)
	return extra
}

