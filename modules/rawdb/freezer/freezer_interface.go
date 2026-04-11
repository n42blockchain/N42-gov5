// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

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
