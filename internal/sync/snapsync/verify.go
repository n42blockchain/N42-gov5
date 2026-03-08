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

package snapsync

import (
	"context"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

var (
	ErrEmptyState     = errors.New("snap sync: downloaded state is empty")
	ErrStateCorrupted = errors.New("snap sync: state data appears corrupted")
)

// StateStats holds statistics about the downloaded state.
type StateStats struct {
	AccountCount uint64
	StorageCount uint64
	CodeCount    uint64
}

// VerifyDownloadedState performs basic integrity checks on the downloaded state.
//
// N42 uses an incremental state root (Keccak hash of dirty accounts per block),
// not a full Merkle Patricia Trie root. This means we cannot directly verify
// the downloaded flat state against a block header's Root field.
//
// Instead, verification relies on:
// 1. Basic integrity: state is non-empty, keys are properly formatted
// 2. Block replay: re-executing blocks from pivot to HEAD validates state
//    transitions, which implicitly verifies the downloaded base state
//
// This function performs check (1). Check (2) happens during the catch-up phase.
func VerifyDownloadedState(ctx context.Context, db kv.RoDB) (*StateStats, error) {
	stats := &StateStats{}

	if err := db.View(ctx, func(tx kv.Tx) error {
		// Count accounts.
		accountCount, err := countTable(tx, modules.Account)
		if err != nil {
			return fmt.Errorf("counting accounts: %w", err)
		}
		stats.AccountCount = accountCount

		// Count storage entries.
		storageCount, err := countTable(tx, modules.Storage)
		if err != nil {
			return fmt.Errorf("counting storage: %w", err)
		}
		stats.StorageCount = storageCount

		// Count code entries.
		codeCount, err := countTable(tx, modules.Code)
		if err != nil {
			return fmt.Errorf("counting code: %w", err)
		}
		stats.CodeCount = codeCount

		return nil
	}); err != nil {
		return nil, err
	}

	if stats.AccountCount == 0 {
		return nil, ErrEmptyState
	}

	log.Info("Downloaded state verification passed",
		"accounts", stats.AccountCount,
		"storageSlots", stats.StorageCount,
		"codes", stats.CodeCount,
	)

	return stats, nil
}

// VerifyAccountIntegrity checks that account keys have valid format (20 bytes).
func VerifyAccountIntegrity(ctx context.Context, db kv.RoDB, sampleSize int) error {
	if sampleSize <= 0 {
		sampleSize = 100
	}

	return db.View(ctx, func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer c.Close()

		checked := 0
		for k, v, err := c.First(); k != nil && checked < sampleSize; k, v, err = c.Next() {
			if err != nil {
				return err
			}
			// Account keys should be 20 bytes (address).
			if len(k) != 20 {
				return fmt.Errorf("%w: account key length %d, expected 20", ErrStateCorrupted, len(k))
			}
			// Account values should not be empty.
			if len(v) == 0 {
				return fmt.Errorf("%w: empty account value for address %x", ErrStateCorrupted, k)
			}
			checked++
		}

		log.Debug("Account integrity check passed", "sampled", checked)
		return nil
	})
}

// countTable counts the number of entries in a database table.
func countTable(tx kv.Tx, table string) (uint64, error) {
	c, err := tx.Cursor(table)
	if err != nil {
		return 0, err
	}
	defer c.Close()

	var count uint64
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}
