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

package bmt

import "lukechampine.com/blake3"

// HashNode computes Blake3(left || right).
func HashNode(left, right []byte) Hash {
	var buf [64]byte
	// left side: if inline, hash it first to get 32B; if already 32B, use directly
	lh := toHash(left)
	rh := toHash(right)
	copy(buf[:32], lh[:])
	copy(buf[32:], rh[:])
	return blake3.Sum256(buf[:])
}

// HashLeaf computes Blake3(0x00 || value) for leaf domain separation.
func HashLeaf(value []byte) Hash {
	h := blake3.New(HashSize, nil)
	h.Write([]byte{0x00}) // domain separator
	h.Write(value)
	var result Hash
	copy(result[:], h.Sum(nil))
	return result
}

// HashKey computes the Blake3 hash of a key for path derivation.
func HashKey(key []byte) Hash {
	return blake3.Sum256(key)
}

// toHash converts a NodeValue to a 32B hash.
// If it's already a hash pointer (32B), return as-is.
// If it's inline data, hash it.
func toHash(v []byte) Hash {
	if len(v) == HashSize {
		var h Hash
		copy(h[:], v)
		return h
	}
	return HashLeaf(v)
}
