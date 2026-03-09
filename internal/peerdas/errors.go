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

package peerdas

import "errors"

var (
	// ErrNilColumn is returned when a nil DataColumn is encountered.
	ErrNilColumn = errors.New("peerdas: data column is nil")

	// ErrColumnIndexOutOfRange is returned when a column index >= NumberOfColumns.
	ErrColumnIndexOutOfRange = errors.New("peerdas: column index out of range")

	// ErrEmptyBlockHash is returned when a DataColumn has a zero block hash.
	ErrEmptyBlockHash = errors.New("peerdas: empty block hash")

	// ErrEmptyKZGProof is returned when a DataColumn has an empty KZG proof.
	ErrEmptyKZGProof = errors.New("peerdas: empty KZG proof")

	// ErrServiceNotEnabled is returned when operations are called on a disabled service.
	ErrServiceNotEnabled = errors.New("peerdas: service not enabled")

	// ErrServiceAlreadyRunning is returned on duplicate Start calls.
	ErrServiceAlreadyRunning = errors.New("peerdas: service already running")
)
