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
// Hash type and Hasher abstraction for the Jellyfish Merkle Tree. Hash is
// a fixed 32-byte Blake3 digest with EmptyHash as the all-zero sentinel
// for empty subtrees. The Hasher interface exposes Hash(data) and
// HashTwo(a, b) so the tree is not hard-wired to any specific digest;
// Blake3Hasher is the default implementation using lukechampine.com/blake3
// and DefaultHasher returns a zero-value instance suitable for sharing.

package jmt

import "lukechampine.com/blake3"

// HashSize is the length of a JMT hash in bytes.
const HashSize = 32

// Hash is a 32-byte Blake3 digest used throughout the JMT.
type Hash [HashSize]byte

// EmptyHash is the zero hash, representing an empty subtree.
var EmptyHash Hash

// Hasher abstracts the hash function so the JMT is not coupled to Blake3.
type Hasher interface {
	// Hash computes a 32-byte digest of the input.
	Hash(data []byte) Hash
	// HashTwo concatenates two hashes and produces a digest.
	HashTwo(a, b Hash) Hash
}

// Blake3Hasher implements Hasher using Blake3-256.
type Blake3Hasher struct{}

var _ Hasher = Blake3Hasher{}

func (Blake3Hasher) Hash(data []byte) Hash {
	return blake3.Sum256(data)
}

func (Blake3Hasher) HashTwo(a, b Hash) Hash {
	var buf [HashSize * 2]byte
	copy(buf[:HashSize], a[:])
	copy(buf[HashSize:], b[:])
	return blake3.Sum256(buf[:])
}

// DefaultHasher returns the default Blake3-based hasher.
func DefaultHasher() Hasher {
	return Blake3Hasher{}
}

// HashKey computes the Blake3 hash of a key (address, storage slot, etc.).
func HashKey(key []byte) Hash {
	return blake3.Sum256(key)
}
