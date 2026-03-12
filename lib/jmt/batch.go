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

import "sort"

// BatchEntry is a single key-value mutation in a batch update.
// A nil Value indicates a deletion.
type BatchEntry struct {
	KeyHash Hash
	Value   []byte // nil means delete
}

// BatchUpdate applies a sorted set of mutations to the tree atomically.
// Entries are sorted by KeyHash for optimal tree traversal (shared-prefix
// locality reduces the number of node loads).
//
// This is the primary API for per-block state updates:
//
//	entries := collectDirtyState(block)
//	root, err := tree.BatchUpdate(entries)
func (t *Tree) BatchUpdate(entries []BatchEntry) (Hash, error) {
	if len(entries) == 0 {
		return t.root, nil
	}

	// Copy to avoid mutating the caller's slice.
	sorted := make([]BatchEntry, len(entries))
	copy(sorted, entries)

	// Sort by key hash to maximize path locality.
	sort.Slice(sorted, func(i, j int) bool {
		return comparHashes(sorted[i].KeyHash, sorted[j].KeyHash) < 0
	})

	// Deduplicate: keep last entry for each key (last-writer-wins).
	deduped := sorted[:0:0]
	for i, e := range sorted {
		if i > 0 && e.KeyHash == sorted[i-1].KeyHash {
			deduped[len(deduped)-1] = e
		} else {
			deduped = append(deduped, e)
		}
	}

	// Apply each mutation sequentially.
	// Future optimization: parallel subtree updates for disjoint nibble prefixes.
	for _, e := range deduped {
		if e.Value == nil {
			if err := t.Delete(e.KeyHash); err != nil && err != ErrNotFound {
				return EmptyHash, err
			}
		} else {
			if err := t.Put(e.KeyHash, e.Value); err != nil {
				return EmptyHash, err
			}
		}
	}

	return t.root, nil
}

// comparHashes returns -1, 0, or 1 comparing two hashes lexicographically.
func comparHashes(a, b Hash) int {
	for i := 0; i < HashSize; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
