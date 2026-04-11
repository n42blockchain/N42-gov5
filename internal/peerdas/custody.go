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
// EIP-7594 custody selection. Derives a per-node deterministic ordering
// of column indices via sha256 over (nodeID, columnIdx), uses the
// columnScore helper to sort the list, and returns the subset a node
// must custody plus the randomised availability sample set. Keeps
// custody decisions stable across restarts so peers can predict each
// other's holdings.

package peerdas

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"sort"

	"github.com/n42blockchain/N42/common/types"
)

// columnScore holds a column index and its deterministic hash score,
// used for sorting during custody and sample selection.
type columnScore struct {
	index uint64
	hash  [32]byte
}

// CustodyColumns returns the column indices that a node with the given
// nodeID must custody. The assignment is deterministic: for each column
// index, we compute sha256(nodeID || big-endian(colIndex)), sort the
// columns by their hash, and return the top custodyCount columns.
//
// custodyCount is clamped to [CustodyRequirement, NumberOfColumns].
func CustodyColumns(nodeID []byte, custodyCount uint64) []uint64 {
	if custodyCount < CustodyRequirement {
		custodyCount = CustodyRequirement
	}
	if custodyCount > NumberOfColumns {
		custodyCount = NumberOfColumns
	}

	scores := make([]columnScore, NumberOfColumns)
	var buf [8]byte
	for i := uint64(0); i < NumberOfColumns; i++ {
		binary.BigEndian.PutUint64(buf[:], i)
		h := sha256.New()
		h.Write(nodeID)
		h.Write(buf[:])
		var digest [32]byte
		h.Sum(digest[:0])
		scores[i] = columnScore{index: i, hash: digest}
	}

	// Sort columns by their hash (lexicographic on the 32-byte digest).
	sort.Slice(scores, func(a, b int) bool {
		return bytes.Compare(scores[a].hash[:], scores[b].hash[:]) < 0
	})

	result := make([]uint64, custodyCount)
	for i := uint64(0); i < custodyCount; i++ {
		result[i] = scores[i].index
	}

	// Sort the result by column index for deterministic ordering.
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// SampleColumns returns deterministic column indices to sample for a
// data availability check on the given block. The selection uses
// sha256(blockHash || counter) to pick count unique columns.
//
// count is clamped to [1, NumberOfColumns].
func SampleColumns(blockHash types.Hash, count int) []uint64 {
	if count <= 0 {
		count = 1
	}
	if count > NumberOfColumns {
		count = NumberOfColumns
	}

	scores := make([]columnScore, NumberOfColumns)
	var buf [8]byte
	for i := uint64(0); i < NumberOfColumns; i++ {
		binary.BigEndian.PutUint64(buf[:], i)
		h := sha256.New()
		h.Write(blockHash[:])
		h.Write(buf[:])
		var digest [32]byte
		h.Sum(digest[:0])
		scores[i] = columnScore{index: i, hash: digest}
	}

	sort.Slice(scores, func(a, b int) bool {
		return bytes.Compare(scores[a].hash[:], scores[b].hash[:]) < 0
	})

	result := make([]uint64, count)
	for i := 0; i < count; i++ {
		result[i] = scores[i].index
	}

	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

