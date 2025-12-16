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
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/hexutil"
)

// PoA consensus protocol constants
const (
	// DefaultEpochLength is the default number of blocks after which to checkpoint
	// and reset the pending votes.
	DefaultEpochLength = uint64(30000)

	// ExtraVanity is the fixed number of extra-data prefix bytes reserved for signer vanity.
	ExtraVanity = 32

	// ExtraSeal is the fixed number of extra-data suffix bytes reserved for signer seal.
	ExtraSeal = crypto.SignatureLength

	// InmemorySnapshots is the number of recent vote snapshots to keep in memory.
	InmemorySnapshots = 128

	// InmemorySignatures is the number of recent block signatures to keep in memory.
	InmemorySignatures = 4096

	// WiggleTime is the random delay (per signer) to allow concurrent signers.
	WiggleTime = 500 * time.Millisecond
)

// PoA magic nonce numbers for voting
var (
	// NonceAuthVote is the magic nonce number to vote on adding a new signer.
	NonceAuthVote = hexutil.MustDecode("0xffffffffffffffff")

	// NonceDropVote is the magic nonce number to vote on removing a signer.
	NonceDropVote = hexutil.MustDecode("0x0000000000000000")
)

// PoA difficulty constants
var (
	// DiffInTurn is the block difficulty for in-turn signatures.
	DiffInTurn = uint256.NewInt(2)

	// DiffNoTurn is the block difficulty for out-of-turn signatures.
	DiffNoTurn = uint256.NewInt(1)
)

