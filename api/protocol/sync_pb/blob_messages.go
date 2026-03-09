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

package sync_pb

// Blob sidecar P2P protocol message types.
// These types implement fastssz.Marshaler/Unmarshaler for P2P encoding.

const (
	// MaxBlobsPerBlock is the maximum number of blobs per block (EIP-4844).
	blobMaxPerBlock = 6

	// BlobDataSize is the size of a single blob in bytes.
	blobDataSize = 131072 // 4096 * 32

	// KZGSize is the byte length of a KZG commitment or proof.
	kzgSize = 48
)

// BlobSidecarsByRangeRequest requests blob sidecars for a range of blocks.
type BlobSidecarsByRangeRequest struct {
	StartBlock uint64 // inclusive start block number
	Count      uint64 // number of blocks to fetch sidecars for
}

// BlobSidecarsByRootRequest requests specific blob sidecars by block root and index.
type BlobSidecarsByRootRequest struct {
	Identifiers []*BlobIdentifierMsg `ssz-max:"64"` // block root + blob index pairs
}

// BlobIdentifierMsg identifies a specific blob.
type BlobIdentifierMsg struct {
	BlockRoot []byte `ssz-size:"32"` // block hash
	Index     uint64 //              blob index within the block
}

// BlobSidecarMsg is the P2P representation of a single blob sidecar.
type BlobSidecarMsg struct {
	Index         uint64 //              blob index within the block
	Blob          []byte `ssz-size:"131072"` // blob data
	KZGCommitment []byte `ssz-size:"48"`     // KZG commitment
	KZGProof      []byte `ssz-size:"48"`     // KZG proof
	BlockNumber   uint64 //              block number
	BlockHash     []byte `ssz-size:"32"`     // block hash
	TxHash        []byte `ssz-size:"32"`     // transaction hash
}

// BlobSidecarsResponse returns a list of blob sidecars.
type BlobSidecarsResponse struct {
	Sidecars []*BlobSidecarMsg `ssz-max:"64"` // max 6 per block, up to ~10 blocks
}
