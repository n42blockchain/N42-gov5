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

package jmt

import "errors"

var (
	// ErrNotFound is returned when a key or node is not in the tree/store.
	ErrNotFound = errors.New("jmt: not found")

	// ErrInvalidNode is returned when node deserialization fails.
	ErrInvalidNode = errors.New("jmt: invalid node encoding")

	// ErrInvalidProof is returned when a Merkle proof fails verification.
	ErrInvalidProof = errors.New("jmt: invalid proof")

	// ErrEmptyBatch is returned when a batch update has no entries.
	ErrEmptyBatch = errors.New("jmt: empty batch")

	// ErrReadOnly is returned when a write is attempted on a read-only store.
	ErrReadOnly = errors.New("jmt: store is read-only")
)
