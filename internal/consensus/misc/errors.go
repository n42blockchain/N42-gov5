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

import "errors"

// Common consensus error definitions shared by PoA consensus engines.
// These should be private to prevent engine specific errors from being referenced
// in the remainder of the codebase, inherently breaking if the engine is swapped out.
var (
	// ErrUnknownBlock is returned when the list of signers is requested for a block
	// that is not part of the local blockchain.
	ErrUnknownBlock = errors.New("unknown block")

	// ErrInvalidCheckpointBeneficiary is returned if a checkpoint/epoch transition
	// block has a beneficiary set to non-zeroes.
	ErrInvalidCheckpointBeneficiary = errors.New("beneficiary in checkpoint block non-zero")

	// ErrInvalidVote is returned if a nonce value is something else that the two
	// allowed constants of 0x00..0 or 0xff..f.
	ErrInvalidVote = errors.New("vote nonce not 0x00..0 or 0xff..f")

	// ErrInvalidCheckpointVote is returned if a checkpoint/epoch transition block
	// has a vote nonce set to non-zeroes.
	ErrInvalidCheckpointVote = errors.New("vote nonce in checkpoint block non-zero")

	// ErrMissingVanity is returned if a block's extra-data section is shorter than
	// 32 bytes, which is required to store the signer vanity.
	ErrMissingVanity = errors.New("extra-data 32 byte vanity prefix missing")

	// ErrMissingSignature is returned if a block's extra-data section doesn't seem
	// to contain a 65 byte secp256k1 signature.
	ErrMissingSignature = errors.New("extra-data 65 byte signature suffix missing")

	// ErrExtraSigners is returned if non-checkpoint block contain signer data in
	// their extra-data fields.
	ErrExtraSigners = errors.New("non-checkpoint block contains extra signer list")

	// ErrInvalidCheckpointSigners is returned if a checkpoint block contains an
	// invalid list of signers (i.e. non divisible by 20 bytes).
	ErrInvalidCheckpointSigners = errors.New("invalid signer list on checkpoint block")

	// ErrMismatchingCheckpointSigners is returned if a checkpoint block contains a
	// list of signers different than the one the local node calculated.
	ErrMismatchingCheckpointSigners = errors.New("mismatching signer list on checkpoint block")

	// ErrInvalidMixDigest is returned if a block's mix digest is non-zero.
	ErrInvalidMixDigest = errors.New("non-zero mix digest")

	// ErrInvalidUncleHash is returned if a block contains an non-empty uncle list.
	ErrInvalidUncleHash = errors.New("non empty uncle hash")

	// ErrInvalidDifficulty is returned if the difficulty of a block neither 1 or 2.
	ErrInvalidDifficulty = errors.New("invalid difficulty")

	// ErrWrongDifficulty is returned if the difficulty of a block doesn't match the
	// turn of the signer.
	ErrWrongDifficulty = errors.New("wrong difficulty")

	// ErrInvalidTimestamp is returned if the timestamp of a block is lower than
	// the previous block's timestamp + the minimum block period.
	ErrInvalidTimestamp = errors.New("invalid timestamp")

	// ErrInvalidVotingChain is returned if an authorization list is attempted to
	// be modified via out-of-range or non-contiguous headers.
	ErrInvalidVotingChain = errors.New("invalid voting chain")

	// ErrUnauthorizedSigner is returned if a header is signed by a non-authorized entity.
	ErrUnauthorizedSigner = errors.New("unauthorized signer")

	// ErrRecentlySigned is returned if a header is signed by an authorized entity
	// that already signed a header recently, thus is temporarily not allowed to.
	ErrRecentlySigned = errors.New("recently signed")

	// ErrFutureBlock is returned when a block's timestamp is in the future.
	ErrFutureBlock = errors.New("block in the future")

	// ErrInvalidGasLimit is returned when gas limit validation fails.
	ErrInvalidGasLimit = errors.New("invalid gas limit")

	// ErrInvalidGasUsed is returned when gas used exceeds gas limit.
	ErrInvalidGasUsed = errors.New("invalid gas used")

	// ErrUnknownAncestor is returned when the parent block is not found.
	ErrUnknownAncestor = errors.New("unknown ancestor")
)

