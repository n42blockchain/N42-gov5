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

package freezer

import (
	"fmt"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/api/protocol/types_pb"
)

// AncientReader provides high-level read access to frozen block data.
type AncientReader struct {
	freezer *Freezer
}

// NewAncientReader wraps a Freezer with typed read methods.
func NewAncientReader(f *Freezer) *AncientReader {
	return &AncientReader{freezer: f}
}

// HasAncient checks whether a block number is in the freezer.
func (r *AncientReader) HasAncient(number uint64) bool {
	return r.freezer.HasAncient(number)
}

// Frozen returns the number of frozen blocks.
func (r *AncientReader) Frozen() uint64 {
	return r.freezer.Frozen()
}

// ReadCanonicalHash retrieves the canonical hash for a frozen block number.
func (r *AncientReader) ReadCanonicalHash(number uint64) (types.Hash, error) {
	data, err := r.freezer.Ancient(TableHashes, number)
	if err != nil {
		return types.Hash{}, err
	}
	if len(data) != 32 {
		return types.Hash{}, fmt.Errorf("freezer: invalid hash length %d for block %d", len(data), number)
	}
	return types.BytesToHash(data), nil
}

// ReadHeaderRaw retrieves the raw protobuf-encoded header for a frozen block.
func (r *AncientReader) ReadHeaderRaw(number uint64) ([]byte, error) {
	return r.freezer.Ancient(TableHeaders, number)
}

// ReadHeader retrieves and decodes a frozen block header.
func (r *AncientReader) ReadHeader(number uint64) (*block.Header, error) {
	raw, err := r.ReadHeaderRaw(number)
	if err != nil {
		return nil, err
	}
	var hPB types_pb.Header
	if err := proto.Unmarshal(raw, &hPB); err != nil {
		return nil, fmt.Errorf("freezer: unmarshal header %d: %w", number, err)
	}
	h := new(block.Header)
	h.FromProtoMessage(&hPB)
	return h, nil
}

// ReadBodyRaw retrieves the raw protobuf-encoded body for a frozen block.
func (r *AncientReader) ReadBodyRaw(number uint64) ([]byte, error) {
	return r.freezer.Ancient(TableBodies, number)
}

// ReadReceiptsRaw retrieves the raw protobuf-encoded receipts for a frozen block.
func (r *AncientReader) ReadReceiptsRaw(number uint64) ([]byte, error) {
	return r.freezer.Ancient(TableReceipts, number)
}

// ReadTd retrieves the total difficulty for a frozen block.
func (r *AncientReader) ReadTd(number uint64) (*uint256.Int, error) {
	data, err := r.freezer.Ancient(TableDifficulty, number)
	if err != nil {
		return nil, err
	}
	td := new(uint256.Int)
	td.SetBytes(data)
	return td, nil
}
