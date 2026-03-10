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

import (
	"errors"
	"fmt"
)

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

	// ErrInvalidKZGProofLength is returned when a KZG proof is not exactly 48 bytes.
	ErrInvalidKZGProofLength = errors.New("peerdas: KZG proof must be exactly 48 bytes")

	// ErrEmptyColumnData is returned when a DataColumn has no cell entries.
	ErrEmptyColumnData = errors.New("peerdas: column data is empty")

	// ErrProofCountMismatch is returned when KZGProofs count != Cells count.
	ErrProofCountMismatch = errors.New("peerdas: KZG proof count does not match cell count")

	// ErrCommitmentCountMismatch is returned when Commitments count != Cells count.
	ErrCommitmentCountMismatch = errors.New("peerdas: commitment count does not match cell count")

	// ErrInvalidCommitmentLength is returned when a commitment is not exactly 48 bytes.
	ErrInvalidCommitmentLength = errors.New("peerdas: commitment must be exactly 48 bytes")

	// ErrKZGVerificationFailed is returned when KZG cell proof verification fails.
	ErrKZGVerificationFailed = errors.New("peerdas: KZG cell proof verification failed")

	// ErrKZGContextNotReady is returned when the PeerDAS KZG context is not initialized.
	ErrKZGContextNotReady = errors.New("peerdas: KZG context not initialized")

	// ErrNoBlobsProvided is returned when no blobs are provided for column production.
	ErrNoBlobsProvided = errors.New("peerdas: no blobs provided")
)

// InvalidCellSizeError provides detailed information about a cell with wrong size.
type InvalidCellSizeError struct {
	Index int
	Got   int
	Want  int
}

func (e *InvalidCellSizeError) Error() string {
	return fmt.Sprintf("peerdas: cell %d has size %d, want %d", e.Index, e.Got, e.Want)
}
