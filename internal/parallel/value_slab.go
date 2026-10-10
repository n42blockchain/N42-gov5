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

package parallel

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
)

// ValueSlab hands out the small values one Block-STM worker records --
// account encodings for reads and writes, balance deltas -- from chunks
// instead of one allocation each: three allocations of a transfer's five.
// Nothing mutates a recorded value, so values share chunks freely; a chunk
// lives while any of its values is referenced. Not safe for concurrent use:
// one slab per worker, shared by its reader and writer.
type ValueSlab struct {
	buf  []byte
	ints []uint256.Int
}

const (
	valueSlabBytes = 16 << 10
	valueSlabInts  = 256
)

// NewValueSlab returns an empty slab.
func NewValueSlab() *ValueSlab { return &ValueSlab{} }

// bytes returns n bytes for a value (n <= valueSlabBytes); full capacity
// is n, so an append cannot run into the next value.
func (s *ValueSlab) bytes(n int) []byte {
	if n > len(s.buf) {
		if n > valueSlabBytes/4 {
			return make([]byte, n)
		}
		s.buf = make([]byte, valueSlabBytes)
	}
	b := s.buf[:n:n]
	s.buf = s.buf[n:]
	return b
}

// int returns a copy of v.
func (s *ValueSlab) int(v *uint256.Int) *uint256.Int {
	if len(s.ints) == 0 {
		s.ints = make([]uint256.Int, valueSlabInts)
	}
	p := &s.ints[0]
	s.ints = s.ints[1:]
	*p = *v
	return p
}

// encodeAccount is acc's V2 encoding from s, or freshly allocated when s
// is nil.
func (s *ValueSlab) encodeAccount(acc *account.StateAccount) []byte {
	if s == nil {
		return acc.MarshalV2()
	}
	b := s.bytes(acc.EncodingLengthForStorageV2())
	acc.EncodeForStorageV2(b)
	return b
}

// cloneInt is a copy of v from s, or freshly allocated when s is nil.
func (s *ValueSlab) cloneInt(v *uint256.Int) *uint256.Int {
	if s == nil {
		return v.Clone()
	}
	return s.int(v)
}
