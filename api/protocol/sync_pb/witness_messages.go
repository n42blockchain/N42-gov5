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

import ssz "github.com/prysmaticlabs/fastssz"

// Witness P2P protocol message types for stateless block verification.
// These types implement fastssz.Marshaler/Unmarshaler for P2P encoding.

// GetBlockWitnessRequest requests a block witness by block hash.
type GetBlockWitnessRequest struct {
	BlockHash []byte `ssz-size:"32"` // hash of the block to get a witness for
}

// MarshalSSZ marshals the GetBlockWitnessRequest to SSZ-encoded bytes.
func (r *GetBlockWitnessRequest) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 32)
	copy(buf, r.BlockHash)
	return buf, nil
}

// MarshalSSZTo marshals the GetBlockWitnessRequest into a pre-allocated buffer.
func (r *GetBlockWitnessRequest) MarshalSSZTo(buf []byte) ([]byte, error) {
	if len(buf) < 32 {
		return nil, ssz.ErrSize
	}
	copy(buf, r.BlockHash)
	return buf[:32], nil
}

// SizeSSZ returns the SSZ encoded size of the request.
func (r *GetBlockWitnessRequest) SizeSSZ() int {
	return 32
}

// UnmarshalSSZ unmarshals from SSZ-encoded bytes.
func (r *GetBlockWitnessRequest) UnmarshalSSZ(buf []byte) error {
	if len(buf) < 32 {
		return ssz.ErrSize
	}
	r.BlockHash = make([]byte, 32)
	copy(r.BlockHash, buf[:32])
	return nil
}
