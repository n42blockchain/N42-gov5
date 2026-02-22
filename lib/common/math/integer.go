// Copyright 2017 The go-ethereum Authors
// (original work)
// Copyright 2024 The Erigon Authors
// (modifications)
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package math

import (
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strconv"
)

// Integer limit values.
// Deprecated: Use the equivalent constants from the standard library math package
// (e.g., math.MaxInt8, math.MinInt8). These are retained for backward compatibility.
const (
	MaxInt8   = math.MaxInt8
	MinInt8   = math.MinInt8
	MaxInt16  = math.MaxInt16
	MinInt16  = math.MinInt16
	MaxInt32  = math.MaxInt32
	MinInt32  = math.MinInt32
	MaxInt64  = math.MaxInt64
	MinInt64  = math.MinInt64
	MaxUint8  = math.MaxUint8
	MaxUint16 = math.MaxUint16
	MaxUint32 = math.MaxUint32
	MaxUint64 = math.MaxUint64
)

// HexOrDecimal64 marshals uint64 as hex or decimal.
type HexOrDecimal64 uint64

// UnmarshalJSON implements json.Unmarshaler.
//
// It is similar to UnmarshalText, but allows parsing real decimals too, not just
// quoted decimal strings.
func (i *HexOrDecimal64) UnmarshalJSON(input []byte) error {
	if len(input) > 1 && input[0] == '"' {
		input = input[1 : len(input)-1]
	}
	return i.UnmarshalText(input)
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (i *HexOrDecimal64) UnmarshalText(input []byte) error {
	n, ok := ParseUint64(string(input))
	if !ok {
		return fmt.Errorf("invalid hex or decimal integer %q", input)
	}
	*i = HexOrDecimal64(n)
	return nil
}

func (i HexOrDecimal64) MarshalText() ([]byte, error) {
	return []byte(fmt.Sprintf("%#x", uint64(i))), nil
}

// ParseUint64 parses s as an integer in decimal or hexadecimal syntax.
// Leading zeros are accepted. The empty string parses as zero.
func ParseUint64(s string) (uint64, bool) {
	if s == "" {
		return 0, true
	}
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		v, err := strconv.ParseUint(s[2:], 16, 64)
		return v, err == nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// MustParseUint64 parses s as an integer and panics if the string is invalid.
func MustParseUint64(s string) uint64 {
	v, ok := ParseUint64(s)
	if !ok {
		panic("invalid unsigned 64 bit integer: " + s)
	}
	return v
}

// AbsoluteDifference is a utility method that given 2 int64, it returns the absolute value of their difference in uint64 format.
func AbsoluteDifference(x, y uint64) uint64 {
	if x > y {
		return x - y
	}
	return y - x
}

func RandInt64() (int64, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return 0, err
	}
	return n.Int64(), nil
}

// SafeMul returns x*y and checks for overflow.
func SafeMul(x, y uint64) (uint64, bool) {
	hi, lo := bits.Mul64(x, y)
	return lo, hi != 0
}

// SafeAdd returns x+y and checks for overflow.
func SafeAdd(x, y uint64) (uint64, bool) {
	sum, carryOut := bits.Add64(x, y, 0)
	return sum, carryOut != 0
}
