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

package freezer

import "context"

// FreezerAPI defines the interface for ancient/freezer cold block storage.
// Implementations store immutable historical block data (headers, bodies,
// receipts, hashes, difficulty) that has been moved out of the hot database.
type FreezerAPI interface {
	// Frozen returns the number of blocks currently stored in the freezer.
	// Blocks 0..Frozen()-1 are available.
	Frozen() uint64

	// HasAncient checks if a block number is available in the freezer.
	HasAncient(number uint64) bool

	// Ancient retrieves raw data from the specified table by block number.
	// Table names are defined as constants: TableHeaders, TableBodies,
	// TableReceipts, TableHashes, TableDifficulty.
	Ancient(table string, number uint64) ([]byte, error)

	// Freeze appends a batch of sequential blocks starting at the given number.
	// The start number must equal Frozen(). All table slices in data must have
	// the same length.
	Freeze(start uint64, data *FreezeData) error

	// TruncateHead removes frozen blocks from the given number onwards.
	// Used during chain reorgs that reach into frozen territory.
	TruncateHead(from uint64) error

	// StartFreeze starts the background goroutine that periodically moves
	// blocks from the hot database into the freezer.
	StartFreeze(ctx context.Context, headFn func() uint64, freezeFn FreezeFunc, cleanupFn func(start, count uint64) error)

	// Sync flushes all tables to disk.
	Sync() error

	// Close stops the background freezer and closes all tables.
	Close() error
}

// Compile-time check that Freezer implements FreezerAPI.
var _ FreezerAPI = (*Freezer)(nil)
