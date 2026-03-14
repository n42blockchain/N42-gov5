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

package types

import (
	"encoding/binary"
	"fmt"
	bloomfilter "github.com/holiman/bloomfilter/v2"
)

const probCollide = 0.0000001

type hasher struct {
	data []byte
}

func newHasher(data []byte) *hasher {
	h := &hasher{}
	_, _ = h.Write(data)
	return h
}

func (h *hasher) Write(p []byte) (n int, err error) {
	h.data = append(h.data[:0], p...)
	return len(p), nil
}

func (h *hasher) Sum(b []byte) []byte {
	var sum [8]byte
	binary.BigEndian.PutUint64(sum[:], h.Sum64())
	return append(b, sum[:]...)
}

func (h *hasher) Reset() {
	h.data = h.data[:0]
}

func (h *hasher) BlockSize() int { return HashLength }
func (h *hasher) Size() int      { return 8 }
func (h *hasher) Sum64() uint64 {
	if len(h.data) < 8 {
		var padded [8]byte
		copy(padded[8-len(h.data):], h.data)
		return binary.BigEndian.Uint64(padded[:])
	}
	return binary.BigEndian.Uint64(h.data)
}

type Bloom struct {
	bloom *bloomfilter.Filter
}

func NewBloom(size uint64) (*Bloom, error) {
	bloom, err := bloomfilter.NewOptimal(size, probCollide)
	if err != nil {
		return nil, err
	}
	return &Bloom{bloom: bloom}, nil
}

func (b *Bloom) UnMarshalBloom(data []byte) error {
	if b.bloom == nil {
		return fmt.Errorf("bloom filter is not initialized")
	}
	return b.bloom.UnmarshalBinary(data)
}

func (b *Bloom) Add(key []byte) error {
	if b.bloom == nil {
		return fmt.Errorf("bloom filter is not initialized")
	}
	if len(key) != HashLength {
		return fmt.Errorf("key length is not %d", HashLength)
	}
	b.bloom.Add(newHasher(key))
	return nil
}

func (b *Bloom) Contain(key []byte) bool {
	if b.bloom == nil {
		return false
	}
	return b.bloom.Contains(newHasher(key))
}

func (b *Bloom) Marshal() ([]byte, error) {
	if b.bloom == nil {
		return nil, fmt.Errorf("bloom filter is not initialized")
	}
	return b.bloom.MarshalBinary()
}
