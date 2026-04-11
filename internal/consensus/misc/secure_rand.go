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
// Cryptographically secure random integer helpers.
// SecureIntn and SecureInt63n return uniform values in [0, n) using
// crypto/rand. Both panic if n <= 0 and on any crand.Int failure,
// which is treated as an unrecoverable entropy outage. Preferred by
// consensus code over math/rand to avoid predictable jitter on PoA
// signer scheduling.

package misc

import (
	crand "crypto/rand"
	"encoding/binary"
	"math/big"
)

// SecureIntn returns a cryptographically secure random integer in [0, n).
// It panics if n <= 0.
func SecureIntn(n int) int {
	if n <= 0 {
		panic("SecureIntn: invalid argument")
	}
	max := big.NewInt(int64(n))
	val, err := crand.Int(crand.Reader, max)
	if err != nil {
		// Fallback: this should never happen with a proper system
		panic("crypto/rand failed: " + err.Error())
	}
	return int(val.Int64())
}

// SecureInt63n returns a cryptographically secure random int64 in [0, n).
// It panics if n <= 0.
func SecureInt63n(n int64) int64 {
	if n <= 0 {
		panic("SecureInt63n: invalid argument")
	}
	max := big.NewInt(n)
	val, err := crand.Int(crand.Reader, max)
	if err != nil {
		// Fallback: this should never happen with a proper system
		panic("crypto/rand failed: " + err.Error())
	}
	return val.Int64()
}

// SecureUint64 returns a cryptographically secure random uint64.
func SecureUint64() uint64 {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return binary.BigEndian.Uint64(b[:])
}

// SecureBytes fills the given byte slice with cryptographically secure random bytes.
func SecureBytes(b []byte) {
	if _, err := crand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
}

// SecureFloat64 returns a cryptographically secure random float64 in [0.0, 1.0).
func SecureFloat64() float64 {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	// Use only 53 bits to ensure uniform distribution in float64
	// (float64 mantissa is 52 bits + implicit leading 1)
	u := binary.BigEndian.Uint64(b[:]) >> 11
	return float64(u) / float64(1<<53)
}

// SecurePerm returns a cryptographically secure random permutation of [0, n).
// It panics if n <= 0.
func SecurePerm(n int) []int {
	if n <= 0 {
		panic("SecurePerm: invalid argument")
	}
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	// Fisher-Yates shuffle with secure random
	for i := n - 1; i > 0; i-- {
		j := SecureIntn(i + 1)
		perm[i], perm[j] = perm[j], perm[i]
	}
	return perm
}

