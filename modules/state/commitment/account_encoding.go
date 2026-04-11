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
// Account leaf value codec for JMT/BMT commitment layers.
// EncodeAccountValue serializes a StateAccount via MarshalV2 for use
// as a tree leaf value using the Erigon-style variable-length encoding.
// DecodeAccountValue restores a StateAccount via DecodeForStorageV2.
// Shared helper used by all non-trie state root computers.

package commitment

import (
	"github.com/n42blockchain/N42/common/account"
)

// EncodeAccountValue serializes a StateAccount using Erigon-style variable-length
// format for storage as a JMT leaf value. Empty fields are omitted.
func EncodeAccountValue(a *account.StateAccount) []byte {
	return a.MarshalV2()
}

// DecodeAccountValue deserializes a JMT leaf value back into a StateAccount.
func DecodeAccountValue(data []byte, a *account.StateAccount) error {
	return a.DecodeForStorageV2(data)
}
