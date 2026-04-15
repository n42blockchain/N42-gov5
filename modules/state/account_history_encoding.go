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

package state

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// EncodeAccountForHistory returns the storage encoding used by account
// changesets/history entries. CodeHash may be intentionally omitted, while the
// reserved V2 incarnation bit is used to persist the historical code version.
func EncodeAccountForHistory(acc *account.StateAccount, omitCodeHash bool, incarnation uint16) []byte {
	if acc == nil || !acc.Initialised {
		return []byte{}
	}

	value := acc.SelfCopy()
	if omitCodeHash {
		copy(value.CodeHash[:], emptyCodeHash)
	}

	n := int(value.EncodingLengthForStorage())
	if incarnation > 0 {
		n += uvarintLen(uint64(incarnation))
	}
	buf := make([]byte, n)
	return buf[:encodeAccountForHistory(buf, value, incarnation)]
}

func encodeAccountForHistory(buf []byte, acc *account.StateAccount, incarnation uint16) int {
	var fieldBits byte
	pos := 1

	if acc.Nonce > 0 {
		fieldBits |= 1
		pos += binary.PutUvarint(buf[pos:], acc.Nonce)
	}
	if !acc.Balance.IsZero() {
		fieldBits |= 2
		balBytes := acc.Balance.Bytes32()
		start := 0
		for start < 31 && balBytes[start] == 0 {
			start++
		}
		trimLen := 32 - start
		buf[pos] = byte(trimLen)
		pos++
		copy(buf[pos:pos+trimLen], balBytes[start:])
		pos += trimLen
	}
	if incarnation > 0 {
		fieldBits |= 4
		pos += binary.PutUvarint(buf[pos:], uint64(incarnation))
	}
	if !acc.IsEmptyCodeHash() {
		fieldBits |= 8
		copy(buf[pos:pos+types.HashLength], acc.CodeHash[:])
		pos += types.HashLength
	}
	buf[0] = fieldBits
	return pos
}

// DecodeAccountHistoryIncarnation extracts the hidden incarnation stored in the
// reserved V2 field-bit slot. It returns (0, false, nil) when the encoding does
// not carry an incarnation tag.
func DecodeAccountHistoryIncarnation(encoded []byte) (uint16, bool, error) {
	if len(encoded) == 0 {
		return 0, false, nil
	}
	// Protobuf payloads do not carry the hidden V2 incarnation tag.
	if encoded[0] > 0x0F {
		return 0, false, nil
	}

	fieldBits := encoded[0]
	pos := 1

	if fieldBits&1 != 0 {
		_, n := binary.Uvarint(encoded[pos:])
		if n <= 0 {
			return 0, false, fmt.Errorf("malformed nonce varint")
		}
		pos += n
	}
	if fieldBits&2 != 0 {
		if pos >= len(encoded) {
			return 0, false, fmt.Errorf("truncated balance length")
		}
		balLen := int(encoded[pos])
		pos++
		if balLen > 32 || pos+balLen > len(encoded) {
			return 0, false, fmt.Errorf("truncated balance data")
		}
		pos += balLen
	}
	if fieldBits&4 == 0 {
		return 0, false, nil
	}
	value, n := binary.Uvarint(encoded[pos:])
	if n <= 0 {
		return 0, false, fmt.Errorf("malformed incarnation varint")
	}
	if value > math.MaxUint16 {
		return 0, false, fmt.Errorf("incarnation overflow: %d", value)
	}
	return uint16(value), true, nil
}

// LookupPlainContractCodeHash resolves the code hash for an address, preferring
// the versioned address+incarnation key and falling back to the legacy addr-only
// mapping for old data.
func LookupPlainContractCodeHash(db kv.Getter, addr []byte, incarnation uint16) ([]byte, error) {
	if incarnation > 0 {
		codeHash, err := db.GetOne(modules.PlainContractCode, modules.PlainGenerateStoragePrefixWithIncarnation(addr, incarnation))
		if err != nil {
			return nil, err
		}
		if len(codeHash) > 0 {
			return codeHash, nil
		}
	}
	return db.GetOne(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(addr))
}

// RestoreHistoricalAccountCodeHash restores an omitted CodeHash from
// PlainContractCode using the hidden historical incarnation tag when present.
// The returned bytes are plain MarshalV2 account bytes suitable for MDBX.
func RestoreHistoricalAccountCodeHash(db kv.Getter, addr, encodedValue []byte) ([]byte, error) {
	var acc account.StateAccount
	if err := acc.DecodeForStorage(encodedValue); err != nil {
		return encodedValue, nil
	}
	if !acc.IsEmptyCodeHash() {
		return acc.MarshalV2(), nil
	}

	incarnation, _, err := DecodeAccountHistoryIncarnation(encodedValue)
	if err != nil {
		return nil, err
	}
	codeHash, err := LookupPlainContractCodeHash(db, addr, incarnation)
	if err != nil {
		return nil, err
	}
	if len(codeHash) > 0 {
		acc.CodeHash = types.BytesToHash(codeHash)
	}
	return acc.MarshalV2(), nil
}

func uvarintLen(v uint64) int {
	var buf [binary.MaxVarintLen64]byte
	return binary.PutUvarint(buf[:], v)
}
