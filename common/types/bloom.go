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

// Collide rate
const probCollide = 0.0000001

type hasher []byte

func (f hasher) Write(p []byte) (n int, err error) { panic("not implemented") }
func (f hasher) Sum(b []byte) []byte               { panic("not implemented") }
func (f hasher) Reset()                            { panic("not implemented") }
func (f hasher) BlockSize() int                    { panic("not implemented") }
func (f hasher) Size() int { return 8 }
func (f hasher) Sum64() uint64 {
	if len(f) < 8 {
		// Pad with zeros if the hasher is too short
		padded := make([]byte, 8)
		copy(padded[8-len(f):], f)
		return binary.BigEndian.Uint64(padded)
	}
	return binary.BigEndian.Uint64(f)
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
	err := b.bloom.UnmarshalBinary(data)
	return err
}

func (b *Bloom) Add(key []byte) error {
	if b.bloom == nil {
		return fmt.Errorf("bloom filter is not initialized")
	}
	if len(key) != HashLength {
		return fmt.Errorf("key length is not 32 ")
	}
	b.bloom.Add(hasher(key))
	return nil
}

// Contain
// - true maybe in the set
// - false must not in the set
func (b *Bloom) Contain(key []byte) bool {
	if b.bloom == nil {
		return false
	}
	return b.bloom.Contains(hasher(key))
}

func (b *Bloom) Marshal() ([]byte, error) {
	if b.bloom == nil {
		return nil, fmt.Errorf("bloom filter is not initialized")
	}
	return b.bloom.MarshalBinary()
}
