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

package commitment

import (
	"encoding/binary"
	"errors"

	"github.com/n42blockchain/N42/common/account"
)

// EncodeAccountValue serializes a StateAccount into a compact binary format
// for storage as a JMT leaf value.
//
// Format (deterministic, fixed-field order):
//
//	[flags:1][nonce:8][balance:32][incarnation:2][codeHash:32][root:32]
//
// flags byte: bit 0 = Initialised
// Total: 1 + 8 + 32 + 2 + 32 + 32 = 107 bytes
const accountEncodingSize = 107

func EncodeAccountValue(a *account.StateAccount) []byte {
	buf := make([]byte, accountEncodingSize)
	offset := 0

	// flags
	if a.Initialised {
		buf[offset] = 0x01
	}
	offset++

	// nonce
	binary.BigEndian.PutUint64(buf[offset:offset+8], a.Nonce)
	offset += 8

	// balance (uint256 → 32 bytes big-endian)
	a.Balance.WriteToSlice(buf[offset : offset+32])
	offset += 32

	// incarnation
	binary.BigEndian.PutUint16(buf[offset:offset+2], a.Incarnation)
	offset += 2

	// codeHash
	copy(buf[offset:offset+32], a.CodeHash[:])
	offset += 32

	// root
	copy(buf[offset:offset+32], a.Root[:])
	return buf
}

// DecodeAccountValue deserializes a JMT leaf value back into a StateAccount.
func DecodeAccountValue(data []byte, a *account.StateAccount) error {
	if len(data) < accountEncodingSize {
		return errors.New("commitment: account data too short")
	}
	offset := 0

	// flags
	a.Initialised = data[offset]&0x01 != 0
	offset++

	// nonce
	a.Nonce = binary.BigEndian.Uint64(data[offset : offset+8])
	offset += 8

	// balance
	a.Balance.SetBytes(data[offset : offset+32])
	offset += 32

	// incarnation
	a.Incarnation = binary.BigEndian.Uint16(data[offset : offset+2])
	offset += 2

	// codeHash
	copy(a.CodeHash[:], data[offset:offset+32])
	offset += 32

	// root
	copy(a.Root[:], data[offset:offset+32])
	return nil
}
