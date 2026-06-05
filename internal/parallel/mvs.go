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

// Package parallel implements Block-STM optimistic parallel transaction execution.
//
// Block-STM executes all transactions in a block concurrently, each writing
// to a multi-version data store (MVS). After execution, transactions are
// validated in order: if tx[i] read a value that was later written by tx[j<i],
// tx[i] must re-execute. This repeats until all transactions are consistent.
package parallel

import (
	"sort"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// LocationKey uniquely identifies a state location that can be read or written.
// It represents either an account-level property or a storage slot.
type LocationKey struct {
	Address types.Address
	Field   LocationField
	Slot    types.Hash // only used for FieldStorage
}

// LocationField identifies which part of an account is being accessed.
type LocationField uint8

const (
	FieldBalance     LocationField = 0
	FieldNonce       LocationField = 1
	FieldCode        LocationField = 2
	FieldCodeHash    LocationField = 3
	FieldStorage     LocationField = 4
	FieldSuicide     LocationField = 5
	FieldExist       LocationField = 6
	FieldCodeSize    LocationField = 7
	FieldIncarnation LocationField = 8
	// FieldStorageWipe is a per-address marker (no slot) recorded whenever an
	// address's whole storage is wiped — SELFDESTRUCT, CREATE-on-existing, or
	// fresh CREATE. It shadows storage reads of that address so a concurrent /
	// later tx never sees stale pre-wipe slot data. Mirrors the mvKeyTagWipe
	// poison-write in modules/state's MV adapter (the production parallel path).
	FieldStorageWipe LocationField = 9
)

// versionedValue is a single write to a location at a given transaction index.
type versionedValue struct {
	txIndex     int
	incarnation uint32 // incarnation of the writing tx
	value       []byte // serialized value; nil means "deleted"
}

// mvEntry holds all versions for one location, sorted by txIndex ascending.
type mvEntry struct {
	mu       sync.RWMutex
	versions []versionedValue
}

// MVS (Multi-Version Store) is the central data structure for Block-STM.
// It maintains a versioned view of all state locations, keyed by (address, field, slot).
// Each transaction writes to its own "version", and reads find the most recent
// write from a preceding transaction.
type MVS struct {
	mu      sync.RWMutex
	entries map[LocationKey]*mvEntry
}

// NewMVS creates a new empty multi-version store.
func NewMVS() *MVS {
	return &MVS{
		entries: make(map[LocationKey]*mvEntry),
	}
}

// getOrCreateEntry returns the mvEntry for a key, creating it if needed.
func (m *MVS) getOrCreateEntry(key LocationKey) *mvEntry {
	m.mu.RLock()
	e, ok := m.entries[key]
	m.mu.RUnlock()
	if ok {
		return e
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Double-check after acquiring write lock.
	if e, ok := m.entries[key]; ok {
		return e
	}
	e = &mvEntry{}
	m.entries[key] = e
	return e
}

// Write records that transaction txIndex (at given incarnation) wrote value to the given location.
// If txIndex already has a write here, it is overwritten (for re-execution).
func (m *MVS) Write(key LocationKey, txIndex int, incarnation uint32, value []byte) {
	e := m.getOrCreateEntry(key)
	e.mu.Lock()
	defer e.mu.Unlock()

	// Find or insert.
	idx := sort.Search(len(e.versions), func(i int) bool {
		return e.versions[i].txIndex >= txIndex
	})

	// Copy value to avoid external mutation.
	var vc []byte
	if value != nil {
		vc = make([]byte, len(value))
		copy(vc, value)
	}

	if idx < len(e.versions) && e.versions[idx].txIndex == txIndex {
		// Overwrite existing.
		e.versions[idx].value = vc
		e.versions[idx].incarnation = incarnation
	} else {
		// Insert at sorted position.
		e.versions = append(e.versions, versionedValue{})
		copy(e.versions[idx+1:], e.versions[idx:])
		e.versions[idx] = versionedValue{txIndex: txIndex, incarnation: incarnation, value: vc}
	}
}

// Read returns the value written by the latest transaction before txIndex.
// Returns (value, writerTxIndex, writerIncarnation, true) if found,
// or (nil, 0, 0, false) if no preceding transaction has written to this location.
func (m *MVS) Read(key LocationKey, txIndex int) ([]byte, int, uint32, bool) {
	m.mu.RLock()
	e, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok {
		return nil, 0, 0, false
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Find the latest version with txIndex < callerTxIndex.
	idx := sort.Search(len(e.versions), func(i int) bool {
		return e.versions[i].txIndex >= txIndex
	})

	if idx == 0 {
		return nil, 0, 0, false
	}

	v := e.versions[idx-1]
	return v.value, v.txIndex, v.incarnation, true
}

// Delete removes all writes by txIndex from the store (used before re-execution).
func (m *MVS) Delete(key LocationKey, txIndex int) {
	m.mu.RLock()
	e, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	idx := sort.Search(len(e.versions), func(i int) bool {
		return e.versions[i].txIndex >= txIndex
	})
	if idx < len(e.versions) && e.versions[idx].txIndex == txIndex {
		e.versions = append(e.versions[:idx], e.versions[idx+1:]...)
	}
}

// ApplyAll iterates over all MVS entries and calls fn with the final
// (highest txIndex < numTxs) value for each location. This is used after
// parallel execution to replay the validated state to the real StateWriter.
func (m *MVS) ApplyAll(numTxs int, fn func(LocationKey, []byte) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for key, e := range m.entries {
		e.mu.RLock()
		// Find the latest version with txIndex < numTxs.
		idx := sort.Search(len(e.versions), func(i int) bool {
			return e.versions[i].txIndex >= numTxs
		})
		if idx > 0 {
			// Copy value while holding the lock to avoid data race.
			src := e.versions[idx-1].value
			val := make([]byte, len(src))
			copy(val, src)
			e.mu.RUnlock()
			if err := fn(key, val); err != nil {
				return err
			}
		} else {
			e.mu.RUnlock()
		}
	}
	return nil
}

// DeleteAll removes all writes by txIndex across all locations.
// We collect entries under RLock first, then release it before locking
// individual entries to prevent deadlock with getOrCreateEntry's Lock.
func (m *MVS) DeleteAll(txIndex int) {
	m.mu.RLock()
	snapshot := make([]*mvEntry, 0, len(m.entries))
	for _, e := range m.entries {
		snapshot = append(snapshot, e)
	}
	m.mu.RUnlock()

	for _, e := range snapshot {
		e.mu.Lock()
		idx := sort.Search(len(e.versions), func(i int) bool {
			return e.versions[i].txIndex >= txIndex
		})
		if idx < len(e.versions) && e.versions[idx].txIndex == txIndex {
			e.versions = append(e.versions[:idx], e.versions[idx+1:]...)
		}
		e.mu.Unlock()
	}
}
