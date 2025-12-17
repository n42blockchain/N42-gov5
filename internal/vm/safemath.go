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

package vm

import (
	"math"

	"github.com/holiman/uint256"
)

// SafeUint64ToInt64 safely converts uint64 to int64.
// Returns the value and true if conversion is safe, or 0 and false if overflow would occur.
func SafeUint64ToInt64(v uint64) (int64, bool) {
	if v > math.MaxInt64 {
		return 0, false
	}
	return int64(v), true
}

// SafeUint64ToInt safely converts uint64 to int.
// Returns the value and true if conversion is safe, or 0 and false if overflow would occur.
func SafeUint64ToInt(v uint64) (int, bool) {
	if v > uint64(math.MaxInt) {
		return 0, false
	}
	return int(v), true
}

// SafeUint64ToUint32 safely converts uint64 to uint32.
// Returns the value and true if conversion is safe, or 0 and false if overflow would occur.
func SafeUint64ToUint32(v uint64) (uint32, bool) {
	if v > math.MaxUint32 {
		return 0, false
	}
	return uint32(v), true
}

// SafeInt64ToInt safely converts int64 to int.
// Returns the value and true if conversion is safe, or 0 and false if overflow would occur.
func SafeInt64ToInt(v int64) (int, bool) {
	if v > int64(math.MaxInt) || v < int64(math.MinInt) {
		return 0, false
	}
	return int(v), true
}

// SafeUint256ToInt64 safely converts uint256.Int to int64.
// Returns the value and true if conversion is safe, or 0 and false if overflow would occur.
func SafeUint256ToInt64(v *uint256.Int) (int64, bool) {
	if !v.IsUint64() {
		return 0, false
	}
	u64 := v.Uint64()
	if u64 > math.MaxInt64 {
		return 0, false
	}
	return int64(u64), true
}

// SafeUint256ToUint64 safely converts uint256.Int to uint64.
// Returns the value and true if conversion is safe, or 0 and false if no overflow.
func SafeUint256ToUint64(v *uint256.Int) (uint64, bool) {
	if !v.IsUint64() {
		return 0, false
	}
	return v.Uint64(), true
}

// MustSafeUint64ToInt64 converts uint64 to int64, clamping to MaxInt64 if overflow would occur.
func MustSafeUint64ToInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// MustSafeUint64ToInt converts uint64 to int, clamping to MaxInt if overflow would occur.
func MustSafeUint64ToInt(v uint64) int {
	if v > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(v)
}

