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

package jmt

// NibbleCount is the number of nibbles in a 32-byte hash (64 half-bytes).
const NibbleCount = HashSize * 2

// NibblePath represents a path through the 16-ary JMT as a sequence of nibbles.
// Each nibble is a value in [0, 15]. The path is derived from a Blake3 hash
// and has exactly 64 nibbles for a 32-byte key.
type NibblePath struct {
	data   [HashSize]byte // compact storage: 2 nibbles per byte
	length int            // number of valid nibbles (0..64)
}

// NewNibblePath creates a full nibble path from a 32-byte hash.
func NewNibblePath(h Hash) NibblePath {
	return NibblePath{data: h, length: NibbleCount}
}

// NewNibblePathFromSlice creates a nibble path from an explicit nibble slice.
// Each element must be in [0, 15].
func NewNibblePathFromSlice(nibbles []byte) NibblePath {
	if len(nibbles) > NibbleCount {
		panic("nibble path exceeds maximum length")
	}
	np := NibblePath{length: len(nibbles)}
	for i, n := range nibbles {
		byteIdx := i / 2
		if i%2 == 0 {
			np.data[byteIdx] = (n & 0x0F) << 4
		} else {
			np.data[byteIdx] |= n & 0x0F
		}
	}
	return np
}

// Len returns the number of nibbles in the path.
func (np NibblePath) Len() int { return np.length }

// At returns the nibble at position i (0-indexed). Panics if out of range.
func (np NibblePath) At(i int) byte {
	if i >= np.length {
		panic("nibble index out of range")
	}
	b := np.data[i/2]
	if i%2 == 0 {
		return (b >> 4) & 0x0F
	}
	return b & 0x0F
}

// Slice returns a sub-path from index start (inclusive) to end (exclusive).
func (np NibblePath) Slice(start, end int) NibblePath {
	if start < 0 || end > np.length || start > end {
		panic("nibble slice out of range")
	}
	nibbles := make([]byte, end-start)
	for i := start; i < end; i++ {
		nibbles[i-start] = np.At(i)
	}
	return NewNibblePathFromSlice(nibbles)
}

// Suffix returns the path from index start to the end.
func (np NibblePath) Suffix(start int) NibblePath {
	return np.Slice(start, np.length)
}

// CommonPrefixLen returns the length of the common prefix between two paths.
func (np NibblePath) CommonPrefixLen(other NibblePath) int {
	maxLen := np.length
	if other.length < maxLen {
		maxLen = other.length
	}
	for i := 0; i < maxLen; i++ {
		if np.At(i) != other.At(i) {
			return i
		}
	}
	return maxLen
}

// Equal returns true if two paths have identical nibbles.
func (np NibblePath) Equal(other NibblePath) bool {
	if np.length != other.length {
		return false
	}
	return np.CommonPrefixLen(other) == np.length
}

// Nibbles returns the path as an explicit nibble slice.
func (np NibblePath) Nibbles() []byte {
	out := make([]byte, np.length)
	for i := 0; i < np.length; i++ {
		out[i] = np.At(i)
	}
	return out
}

// Append returns a new path with the given nibble appended.
func (np NibblePath) Append(nibble byte) NibblePath {
	nibbles := np.Nibbles()
	nibbles = append(nibbles, nibble&0x0F)
	return NewNibblePathFromSlice(nibbles)
}

// Concat returns a new path that is np followed by other.
func (np NibblePath) Concat(other NibblePath) NibblePath {
	nibbles := make([]byte, np.length+other.length)
	copy(nibbles, np.Nibbles())
	copy(nibbles[np.length:], other.Nibbles())
	return NewNibblePathFromSlice(nibbles)
}
