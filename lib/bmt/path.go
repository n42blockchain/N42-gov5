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
// Bit-path utilities for BMT key traversal. Path stores a packed big-endian
// bit sequence (Bytes + BitLen) up to MaxDepth = 256 bits, matching the
// width of a Blake3 key hash. EmptyPath, FromKeyHash, Bit, and Prefix build
// and slice paths during insert/lookup, letting the tree descend one bit
// at a time from the root down to a leaf without heap-allocating nibbles.

package bmt

// Path operations for BMT key traversal.

// MaxDepth is the maximum tree depth (256-bit key hash = 256 bits).
const MaxDepth = 256

// Path represents a bit-path from root to a node.
type Path struct {
	Bytes  []byte // packed bits, MSB first
	BitLen int    // number of valid bits
}

// EmptyPath returns a zero-length path (root).
func EmptyPath() Path { return Path{} }

// FromKeyHash creates a full 256-bit path from a key hash.
func FromKeyHash(h Hash) Path {
	b := make([]byte, HashSize)
	copy(b, h[:])
	return Path{Bytes: b, BitLen: 256}
}

// Bit returns the bit at position i (0 = MSB of first byte).
func (p Path) Bit(i int) byte {
	if i >= p.BitLen {
		return 0
	}
	return (p.Bytes[i/8] >> (7 - uint(i%8))) & 1
}

// Prefix returns the first n bits as a new Path.
func (p Path) Prefix(n int) Path {
	if n >= p.BitLen {
		return p
	}
	byteLen := (n + 7) / 8
	result := make([]byte, byteLen)
	copy(result, p.Bytes[:byteLen])
	// mask off trailing bits in last byte
	if rem := n % 8; rem != 0 {
		result[byteLen-1] &= ^byte((1 << (8 - uint(rem))) - 1)
	}
	return Path{Bytes: result, BitLen: n}
}

// Append adds a bit (0 or 1) to the path.
func (p Path) Append(bit byte) Path {
	newBitLen := p.BitLen + 1
	newByteLen := (newBitLen + 7) / 8
	result := make([]byte, newByteLen)
	copy(result, p.Bytes)
	if bit == 1 {
		byteIdx := p.BitLen / 8
		bitIdx := 7 - uint(p.BitLen%8)
		result[byteIdx] |= 1 << bitIdx
	}
	return Path{Bytes: result, BitLen: newBitLen}
}

// Sibling flips the last bit.
func (p Path) Sibling() Path {
	if p.BitLen == 0 {
		return p
	}
	result := make([]byte, len(p.Bytes))
	copy(result, p.Bytes)
	byteIdx := (p.BitLen - 1) / 8
	bitIdx := 7 - uint((p.BitLen-1)%8)
	result[byteIdx] ^= 1 << bitIdx
	return Path{Bytes: result, BitLen: p.BitLen}
}

// Equal checks path equality.
func (p Path) Equal(other Path) bool {
	if p.BitLen != other.BitLen {
		return false
	}
	for i := 0; i < len(p.Bytes) && i < len(other.Bytes); i++ {
		if p.Bytes[i] != other.Bytes[i] {
			return false
		}
	}
	return true
}

// String returns a key suitable for map lookups.
func (p Path) String() string {
	return string(append([]byte{byte(p.BitLen)}, p.Bytes...))
}
