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

package rawdb

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// WriteBlockAccessList stores a block's raw EIP-7928 BAL (canonical RLP) keyed by
// block hash, so the out-of-band BAL service can serve it to peers without
// re-deriving it. Empty raw is a no-op.
func WriteBlockAccessList(db kv.Putter, hash types.Hash, raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	return db.Put(modules.BlockAccessLists, hash[:], raw)
}

// ReadBlockAccessList returns a block's stored raw BAL, or nil if none is stored.
func ReadBlockAccessList(db kv.Getter, hash types.Hash) []byte {
	raw, err := db.GetOne(modules.BlockAccessLists, hash[:])
	if err != nil || len(raw) == 0 {
		return nil
	}
	return raw
}

// DeleteBlockAccessList removes a stored raw BAL (used on unwind/prune).
func DeleteBlockAccessList(db kv.Deleter, hash types.Hash) error {
	return db.Delete(modules.BlockAccessLists, hash[:])
}
