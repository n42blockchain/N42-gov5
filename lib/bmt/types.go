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

package bmt

import (
	"errors"

	"github.com/n42blockchain/N42/common/types"
)

// HashSize is the length of a BMT hash in bytes.
const HashSize = 32

// Hash is a 32-byte Blake3 digest used throughout the BMT.
type Hash = types.Hash

var (
	// EmptyHash is the zero hash, representing an empty subtree.
	EmptyHash = Hash{}

	// ErrNotFound is returned when a node is not present in the store.
	ErrNotFound = errors.New("bmt: not found")
)

// NodeValue is stored in the tree. Length distinguishes type:
//
//	== 32: internal node (hash of left||right)
//	< 32:  inline leaf (V2 encoded account/storage value)
//	== 33: leaf whose data is exactly 32 bytes (data + 0x01 tag)
//	> 33:  extended leaf data
type NodeValue []byte

// IsHashPointer returns true if this value is exactly 32 bytes (an internal node hash).
func (v NodeValue) IsHashPointer() bool { return len(v) == HashSize }

// IsInline returns true if this value is non-empty and not a hash pointer.
func (v NodeValue) IsInline() bool { return len(v) > 0 && len(v) != HashSize }
