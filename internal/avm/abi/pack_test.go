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

package abi

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/internal/avm/common"
)

func TestPack(t *testing.T) {
	for i, test := range packUnpackTests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			encb, err := hex.DecodeString(test.packed)
			if err != nil {
				t.Fatalf("invalid hex %s: %v", test.packed, err)
			}
			inDef := fmt.Sprintf(`[{ "name" : "method", "type": "function", "inputs": %s}]`, test.def)
			inAbi, err := JSON(strings.NewReader(inDef))
			if err != nil {
				t.Fatalf("invalid ABI definition %s, %v", inDef, err)
			}
			packed, err := inAbi.Pack("method", test.unpacked)
			if err != nil {
				t.Fatalf("test %d (%v) failed: %v", i, test.def, err)
			}
			if !reflect.DeepEqual(packed[4:], encb) {
				t.Errorf("test %d (%v) failed: expected %v, got %v", i, test.def, encb, packed[4:])
			}
		})
	}
}

func TestPackNumber(t *testing.T) {
	tests := []struct {
		value  reflect.Value
		packed []byte
	}{
		// Protocol limits
		{reflect.ValueOf(0), common.Hex2Bytes("0000000000000000000000000000000000000000000000000000000000000000")},
		{reflect.ValueOf(1), common.Hex2Bytes("0000000000000000000000000000000000000000000000000000000000000001")},
		{reflect.ValueOf(-1), common.Hex2Bytes("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")},

		// Type corner cases
		{reflect.ValueOf(uint8(math.MaxUint8)), common.Hex2Bytes("00000000000000000000000000000000000000000000000000000000000000ff")},
		{reflect.ValueOf(uint16(math.MaxUint16)), common.Hex2Bytes("000000000000000000000000000000000000000000000000000000000000ffff")},
		{reflect.ValueOf(uint32(math.MaxUint32)), common.Hex2Bytes("00000000000000000000000000000000000000000000000000000000ffffffff")},
		{reflect.ValueOf(uint64(math.MaxUint64)), common.Hex2Bytes("000000000000000000000000000000000000000000000000ffffffffffffffff")},

		{reflect.ValueOf(int8(math.MaxInt8)), common.Hex2Bytes("000000000000000000000000000000000000000000000000000000000000007f")},
		{reflect.ValueOf(int16(math.MaxInt16)), common.Hex2Bytes("0000000000000000000000000000000000000000000000000000000000007fff")},
		{reflect.ValueOf(int32(math.MaxInt32)), common.Hex2Bytes("000000000000000000000000000000000000000000000000000000007fffffff")},
		{reflect.ValueOf(int64(math.MaxInt64)), common.Hex2Bytes("0000000000000000000000000000000000000000000000007fffffffffffffff")},

		{reflect.ValueOf(int8(math.MinInt8)), common.Hex2Bytes("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff80")},
		{reflect.ValueOf(int16(math.MinInt16)), common.Hex2Bytes("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff8000")},
		{reflect.ValueOf(int32(math.MinInt32)), common.Hex2Bytes("ffffffffffffffffffffffffffffffffffffffffffffffffffffffff80000000")},
		{reflect.ValueOf(int64(math.MinInt64)), common.Hex2Bytes("ffffffffffffffffffffffffffffffffffffffffffffffff8000000000000000")},
	}
	for i, tt := range tests {
		packed, err := packNum(tt.value)
		if err != nil {
			t.Errorf("test %d: packNum error: %v", i, err)
			continue
		}
		if !bytes.Equal(packed, tt.packed) {
			t.Errorf("test %d: pack mismatch: have %x, want %x", i, packed, tt.packed)
		}
	}
}
