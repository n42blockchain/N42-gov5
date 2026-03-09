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

package rawdb

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// blobSidecarKey returns the database key for blob sidecars: block_num(8) + hash(32).
func blobSidecarKey(blockNum uint64, blockHash types.Hash) []byte {
	key := make([]byte, 8+32)
	binary.BigEndian.PutUint64(key[:8], blockNum)
	copy(key[8:], blockHash.Bytes())
	return key
}

// WriteBlobSidecars stores blob sidecars for a block.
// The sidecars are serialized with a simple length-prefixed binary format.
func WriteBlobSidecars(tx kv.RwTx, blockNum uint64, blockHash types.Hash, sidecars []*block.BlobSidecar) error {
	if len(sidecars) == 0 {
		return nil
	}

	data, err := encodeBlobSidecars(sidecars)
	if err != nil {
		return fmt.Errorf("failed to encode blob sidecars: %w", err)
	}

	key := blobSidecarKey(blockNum, blockHash)
	return tx.Put(modules.BlobSidecars, key, data)
}

// ReadBlobSidecars reads blob sidecars for a block.
func ReadBlobSidecars(tx kv.Tx, blockNum uint64, blockHash types.Hash) ([]*block.BlobSidecar, error) {
	key := blobSidecarKey(blockNum, blockHash)
	data, err := tx.GetOne(modules.BlobSidecars, key)
	if err != nil {
		return nil, fmt.Errorf("failed to read blob sidecars: %w", err)
	}
	if data == nil {
		return nil, nil
	}

	sidecars, err := decodeBlobSidecars(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode blob sidecars: %w", err)
	}
	return sidecars, nil
}

// DeleteBlobSidecars removes blob sidecars for a block.
func DeleteBlobSidecars(tx kv.RwTx, blockNum uint64, blockHash types.Hash) error {
	key := blobSidecarKey(blockNum, blockHash)
	return tx.Delete(modules.BlobSidecars, key)
}

// --- Binary encoding for blob sidecars ---
//
// Format:
//   [4 bytes] count (uint32 little-endian)
//   For each sidecar:
//     [8 bytes]   Index
//     [131072 bytes] Blob
//     [48 bytes]  KZGCommitment
//     [48 bytes]  KZGProof
//     [8 bytes]   BlockNumber
//     [32 bytes]  BlockHash
//     [32 bytes]  TxHash
//     [4 bytes]   ProofCount (uint32)
//     For each proof element:
//       [32 bytes] proof hash

var (
	errBlobDecodeShort = errors.New("blob sidecar decode: buffer too short")
	errBlobDecodeBad   = errors.New("blob sidecar decode: too many entries")
)

const maxBlobSidecars = 64 // safety limit

func encodeBlobSidecars(sidecars []*block.BlobSidecar) ([]byte, error) {
	// Estimate size: 4 + count * (8 + 131072 + 48 + 48 + 8 + 32 + 32 + 4)
	// plus proof entries
	estSize := 4
	for _, sc := range sidecars {
		estSize += 8 + block.BlobSize + block.KZGCommitmentSize + block.KZGProofSize + 8 + 32 + 32 + 4
		estSize += len(sc.CommitmentInclusionProof) * 32
	}

	buf := make([]byte, 0, estSize)

	// Count
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, uint32(len(sidecars)))
	buf = append(buf, countBuf...)

	u64Buf := make([]byte, 8)
	u32Buf := make([]byte, 4)

	for _, sc := range sidecars {
		// Index
		binary.LittleEndian.PutUint64(u64Buf, sc.Index)
		buf = append(buf, u64Buf...)

		// Blob
		buf = append(buf, sc.Blob[:]...)

		// KZGCommitment
		buf = append(buf, sc.KZGCommitment[:]...)

		// KZGProof
		buf = append(buf, sc.KZGProof[:]...)

		// BlockNumber
		binary.LittleEndian.PutUint64(u64Buf, sc.BlockNumber)
		buf = append(buf, u64Buf...)

		// BlockHash
		buf = append(buf, sc.BlockHash.Bytes()...)

		// TxHash
		buf = append(buf, sc.TxHash.Bytes()...)

		// CommitmentInclusionProof count + elements
		binary.LittleEndian.PutUint32(u32Buf, uint32(len(sc.CommitmentInclusionProof)))
		buf = append(buf, u32Buf...)
		for _, proof := range sc.CommitmentInclusionProof {
			buf = append(buf, proof[:]...)
		}
	}

	return buf, nil
}

func decodeBlobSidecars(data []byte) ([]*block.BlobSidecar, error) {
	if len(data) < 4 {
		return nil, errBlobDecodeShort
	}

	count := binary.LittleEndian.Uint32(data[:4])
	if count > maxBlobSidecars {
		return nil, errBlobDecodeBad
	}
	off := 4

	sidecars := make([]*block.BlobSidecar, count)
	for i := uint32(0); i < count; i++ {
		sc := &block.BlobSidecar{}

		// Index (8)
		if off+8 > len(data) {
			return nil, errBlobDecodeShort
		}
		sc.Index = binary.LittleEndian.Uint64(data[off : off+8])
		off += 8

		// Blob (131072)
		if off+block.BlobSize > len(data) {
			return nil, errBlobDecodeShort
		}
		copy(sc.Blob[:], data[off:off+block.BlobSize])
		off += block.BlobSize

		// KZGCommitment (48)
		if off+block.KZGCommitmentSize > len(data) {
			return nil, errBlobDecodeShort
		}
		copy(sc.KZGCommitment[:], data[off:off+block.KZGCommitmentSize])
		off += block.KZGCommitmentSize

		// KZGProof (48)
		if off+block.KZGProofSize > len(data) {
			return nil, errBlobDecodeShort
		}
		copy(sc.KZGProof[:], data[off:off+block.KZGProofSize])
		off += block.KZGProofSize

		// BlockNumber (8)
		if off+8 > len(data) {
			return nil, errBlobDecodeShort
		}
		sc.BlockNumber = binary.LittleEndian.Uint64(data[off : off+8])
		off += 8

		// BlockHash (32)
		if off+32 > len(data) {
			return nil, errBlobDecodeShort
		}
		sc.BlockHash = types.BytesToHash(data[off : off+32])
		off += 32

		// TxHash (32)
		if off+32 > len(data) {
			return nil, errBlobDecodeShort
		}
		sc.TxHash = types.BytesToHash(data[off : off+32])
		off += 32

		// CommitmentInclusionProof count (4)
		if off+4 > len(data) {
			return nil, errBlobDecodeShort
		}
		proofCount := binary.LittleEndian.Uint32(data[off : off+4])
		off += 4
		if proofCount > 256 { // safety limit
			return nil, errBlobDecodeBad
		}

		if proofCount > 0 {
			if off+int(proofCount)*32 > len(data) {
				return nil, errBlobDecodeShort
			}
			sc.CommitmentInclusionProof = make([][32]byte, proofCount)
			for j := uint32(0); j < proofCount; j++ {
				copy(sc.CommitmentInclusionProof[j][:], data[off:off+32])
				off += 32
			}
		}

		sidecars[i] = sc
	}

	return sidecars, nil
}
