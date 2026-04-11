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
//
// BatchProposal type for multi-block HotStuff-2 rounds.
// Defines MaxBatchSize = 16 and error sentinels ErrEmptyBatch,
// ErrMismatchedRoots and ErrBatchTooLarge. Carries View, block and
// tx-root hash slices, the justifying QC, proposer index and
// signature. Design-only scaffolding for future Autobahn-style
// multi-proposer extensions to the core engine.

package hotstuff

import (
	"encoding/binary"
	"errors"

	"github.com/n42blockchain/N42/common/types"
)

var (
	// ErrEmptyBatch is returned when a batch proposal contains no blocks.
	ErrEmptyBatch = errors.New("batch proposal: no blocks")
	// ErrMismatchedRoots is returned when block count != tx root count.
	ErrMismatchedRoots = errors.New("batch proposal: block count != tx root count")
	// ErrBatchTooLarge is returned when a batch exceeds MaxBatchSize.
	ErrBatchTooLarge = errors.New("batch proposal: exceeds max batch size")
)

// MaxBatchSize is the maximum number of blocks in a single batch proposal.
const MaxBatchSize = 16

// BatchProposal supports multiple blocks in a single consensus round.
// This is a design-only type for future multi-proposer (Autobahn) support.
type BatchProposal struct {
	View         ViewNumber
	BlockHashes  []types.Hash
	TxRootHashes []types.Hash
	JustifyQC    QuorumCertificate
	Proposer     ValidatorIndex
	Signature    []byte
}

// BatchSigningMessage builds the message to sign for a batch:
// "batch" (5 bytes) || view (8 bytes LE) || count (4 bytes LE) || hash1 (32 bytes) || hash2 (32 bytes) || ...
func BatchSigningMessage(view ViewNumber, hashes []types.Hash) []byte {
	// prefix(5) + view(8) + count(4) + hashes(32 each)
	size := 5 + 8 + 4 + len(hashes)*32
	msg := make([]byte, size)

	copy(msg[:5], []byte("batch"))
	binary.LittleEndian.PutUint64(msg[5:13], view)
	binary.LittleEndian.PutUint32(msg[13:17], uint32(len(hashes)))

	offset := 17
	for _, h := range hashes {
		copy(msg[offset:offset+32], h[:])
		offset += 32
	}
	return msg
}

// ValidateBatch checks the structural validity of a batch proposal.
func ValidateBatch(bp *BatchProposal) error {
	if len(bp.BlockHashes) == 0 {
		return ErrEmptyBatch
	}
	if len(bp.BlockHashes) != len(bp.TxRootHashes) {
		return ErrMismatchedRoots
	}
	if len(bp.BlockHashes) > MaxBatchSize {
		return ErrBatchTooLarge
	}
	return nil
}
