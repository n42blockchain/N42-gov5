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
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
)

// AccountKeyHash computes the JMT key for an account: Blake3(address).
func AccountKeyHash(addr types.Address) jmt.Hash {
	return jmt.HashKey(addr[:])
}

// StorageKeyHash computes the JMT key for a storage slot:
// Blake3(address || slot).
// The concatenation ensures no collision between accounts and storage,
// and between different accounts' storage slots.
func StorageKeyHash(addr types.Address, slot types.Hash) jmt.Hash {
	// 20 (address) + 32 (slot) = 52 bytes
	var buf [52]byte
	copy(buf[:20], addr[:])
	copy(buf[20:], slot[:])
	return jmt.HashKey(buf[:])
}
