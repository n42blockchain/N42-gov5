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
	"bytes"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/api/protocol/sync_pb"
	"github.com/n42blockchain/N42/internal/snapshot"
)

var (
	ErrBatchEmpty        = errors.New("batch response contains no entries")
	ErrBatchCountMismatch = errors.New("batch entry count does not match metadata")
	ErrBatchKeyLength    = errors.New("batch entry has invalid key length")
	ErrBatchKeyOrder     = errors.New("batch entries are not in ascending key order")
	ErrBatchKeyOutOfRange = errors.New("batch entry key is outside requested range")
)

// validateAccountBatch checks the integrity of a compressed account batch response.
// It verifies:
// - entry count matches the response metadata
// - all keys are exactly 20 bytes (address length)
// - keys are in strictly ascending order
// - keys fall within the requested [startKey, endKey) range
func validateAccountBatch(resp *sync_pb.CompressedBatchResponse, entries []snapshot.BatchEntry, startKey, endKey []byte) error {
	if int(resp.EntryCount) != len(entries) {
		return fmt.Errorf("%w: metadata says %d, decoded %d", ErrBatchCountMismatch, resp.EntryCount, len(entries))
	}

	if len(entries) == 0 {
		if !resp.Completed {
			return ErrBatchEmpty
		}
		return nil
	}

	var prevKey []byte
	for i, e := range entries {
		if len(e.Key) != 20 {
			return fmt.Errorf("%w: entry[%d] key length %d", ErrBatchKeyLength, i, len(e.Key))
		}

		// Check ascending order.
		if prevKey != nil && bytes.Compare(e.Key, prevKey) <= 0 {
			return fmt.Errorf("%w: entry[%d] key %x <= prev %x", ErrBatchKeyOrder, i, e.Key, prevKey)
		}
		prevKey = e.Key

		// Check range bounds.
		if len(startKey) > 0 && bytes.Compare(e.Key, startKey) < 0 {
			return fmt.Errorf("%w: entry[%d] key %x < startKey %x", ErrBatchKeyOutOfRange, i, e.Key, startKey)
		}
		if len(endKey) > 0 && bytes.Compare(e.Key, endKey) >= 0 {
			return fmt.Errorf("%w: entry[%d] key %x >= endKey %x", ErrBatchKeyOutOfRange, i, e.Key, endKey)
		}
	}

	return nil
}

// validateStorageBatch checks the integrity of a compressed storage batch response.
// Storage keys (location hashes) are 32 bytes and must be in ascending order.
func validateStorageBatch(resp *sync_pb.CompressedBatchResponse, entries []snapshot.BatchEntry, startKey, endKey []byte) error {
	if int(resp.EntryCount) != len(entries) {
		return fmt.Errorf("%w: metadata says %d, decoded %d", ErrBatchCountMismatch, resp.EntryCount, len(entries))
	}

	if len(entries) == 0 {
		if !resp.Completed {
			return ErrBatchEmpty
		}
		return nil
	}

	var prevKey []byte
	for i, e := range entries {
		if len(e.Key) != 32 {
			return fmt.Errorf("%w: entry[%d] storage key length %d, expected 32", ErrBatchKeyLength, i, len(e.Key))
		}

		if prevKey != nil && bytes.Compare(e.Key, prevKey) <= 0 {
			return fmt.Errorf("%w: entry[%d] key %x <= prev %x", ErrBatchKeyOrder, i, e.Key, prevKey)
		}
		prevKey = e.Key

		if len(startKey) > 0 && bytes.Compare(e.Key, startKey) < 0 {
			return fmt.Errorf("%w: entry[%d] key %x < startKey %x", ErrBatchKeyOutOfRange, i, e.Key, startKey)
		}
		if len(endKey) > 0 && bytes.Compare(e.Key, endKey) >= 0 {
			return fmt.Errorf("%w: entry[%d] key %x >= endKey %x", ErrBatchKeyOutOfRange, i, e.Key, endKey)
		}
	}

	return nil
}

// validateCodeResponse checks that code entries have matching hashes.
func validateCodeResponse(resp *sync_pb.CodeResponse, requestedHashes [][]byte) error {
	if resp == nil || len(resp.Codes) == 0 {
		return nil
	}

	requested := make(map[string]struct{}, len(requestedHashes))
	for _, h := range requestedHashes {
		requested[string(h)] = struct{}{}
	}

	for i, entry := range resp.Codes {
		if len(entry.Hash) != 32 {
			return fmt.Errorf("code entry[%d]: invalid hash length %d", i, len(entry.Hash))
		}
		if _, ok := requested[string(entry.Hash)]; !ok {
			return fmt.Errorf("code entry[%d]: unrequested hash %x", i, entry.Hash)
		}
		if len(entry.Code) == 0 {
			return fmt.Errorf("code entry[%d]: empty code for hash %x", i, entry.Hash)
		}
	}

	return nil
}
