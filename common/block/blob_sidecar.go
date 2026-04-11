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
// EIP-4844 BlobSidecar type and size constants. BlobSize is 4096
// field elements x 32 bytes; KZGCommitmentSize and KZGProofSize are
// each 48 bytes; MaxBlobsPerBlock caps the per-block sidecar count
// and CommitmentInclusionProofDepth fixes the Merkle proof height.
// BlobSidecar bundles the blob, its KZG commitment + proof, the
// owning block number/hash, the originating tx hash and the Merkle
// inclusion proof used by consensus-layer data-availability checks.

package block

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
)

const (
	// BlobSize is the size of a single blob in bytes (4096 field elements * 32 bytes).
	BlobSize = 131072

	// KZGCommitmentSize is the byte length of a KZG commitment.
	KZGCommitmentSize = 48

	// KZGProofSize is the byte length of a KZG proof.
	KZGProofSize = 48

	// MaxBlobsPerBlock is the maximum number of blob sidecars per block.
	MaxBlobsPerBlock = 6

	// CommitmentInclusionProofDepth is the depth of the inclusion proof.
	CommitmentInclusionProofDepth = 17
)

// BlobSidecar contains a blob with its KZG commitment, proof, and the
// signed block header it belongs to, plus a Merkle inclusion proof.
type BlobSidecar struct {
	Index                    uint64              // blob index within the block
	Blob                     [BlobSize]byte      // the actual blob data
	KZGCommitment            [KZGCommitmentSize]byte
	KZGProof                 [KZGProofSize]byte
	BlockNumber              uint64              // block number this sidecar belongs to
	BlockHash                types.Hash          // block hash this sidecar belongs to
	TxHash                   types.Hash          // hash of the blob transaction
	CommitmentInclusionProof [][32]byte          // merkle inclusion proof
}

// BlobIdentifier identifies a specific blob by block root and index.
type BlobIdentifier struct {
	BlockRoot types.Hash
	Index     uint64
}

// Validate performs basic structural validation on the blob sidecar.
func (s *BlobSidecar) Validate() error {
	if s == nil {
		return errors.New("blob sidecar is nil")
	}
	if s.Index >= MaxBlobsPerBlock {
		return fmt.Errorf("blob index %d exceeds maximum %d", s.Index, MaxBlobsPerBlock-1)
	}
	if s.BlockHash == (types.Hash{}) {
		return errors.New("blob sidecar has empty block hash")
	}
	return nil
}

// Copy creates a deep copy of the blob sidecar.
func (s *BlobSidecar) Copy() *BlobSidecar {
	if s == nil {
		return nil
	}
	cpy := &BlobSidecar{
		Index:       s.Index,
		Blob:        s.Blob,
		KZGCommitment: s.KZGCommitment,
		KZGProof:    s.KZGProof,
		BlockNumber: s.BlockNumber,
		BlockHash:   s.BlockHash,
		TxHash:      s.TxHash,
	}
	if len(s.CommitmentInclusionProof) > 0 {
		cpy.CommitmentInclusionProof = make([][32]byte, len(s.CommitmentInclusionProof))
		copy(cpy.CommitmentInclusionProof, s.CommitmentInclusionProof)
	}
	return cpy
}
