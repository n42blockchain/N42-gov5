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

package transaction

// Profile1TxTypeAllowed reports whether typ is a transaction type that is
// valid on an N42 Standard Profile 1 chain (mixed production standard
// section 2(c)): legacy (0x00), access list (0x01), dynamic fee (0x02) and
// set code (0x04). Every other type is refused by pool, builder and block
// validity: blob (0x03, decision O2), gov5's post-quantum type (0x05), the
// N42 range 0x50-0x5F (decision O3 and the reserved-range rules) and any
// unknown type.
func Profile1TxTypeAllowed(typ uint8) bool {
	switch typ {
	case LegacyTxType, AccessListTxType, DynamicFeeTxType, SetCodeTxType:
		return true
	default:
		return false
	}
}
