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

package peerdas

import "github.com/n42blockchain/N42/common/types"

const (
	// NumberOfColumns is the total number of data columns per blob (EIP-7594).
	NumberOfColumns = 128

	// CustodyRequirement is the minimum number of columns each node must custody.
	CustodyRequirement = 4

	// SamplesPerSlot is the default number of columns sampled for an availability check.
	SamplesPerSlot = 8
)

// DataColumn represents a single column of blob data for a given block.
// Each column contains one field element per blob present in the block,
// along with the corresponding KZG proof for that column.
type DataColumn struct {
	Index       uint64     // column index in [0, NumberOfColumns)
	BlockHash   types.Hash // block this column belongs to
	BlockNumber uint64
	// Data holds the column payload. Each inner slice corresponds to a
	// blob in the block (one field-element slice per blob).
	Data [][]byte
	// KZGProof is the aggregated KZG proof for this column.
	KZGProof []byte
}

// DataColumnIdentifier uniquely identifies a data column by block root
// and column index. Used in P2P requests.
type DataColumnIdentifier struct {
	BlockRoot types.Hash
	Index     uint64
}

// Validate performs basic structural validation on a DataColumn.
func (dc *DataColumn) Validate() error {
	if dc == nil {
		return ErrNilColumn
	}
	if dc.Index >= NumberOfColumns {
		return ErrColumnIndexOutOfRange
	}
	if dc.BlockHash == (types.Hash{}) {
		return ErrEmptyBlockHash
	}
	if len(dc.KZGProof) == 0 {
		return ErrEmptyKZGProof
	}
	return nil
}
