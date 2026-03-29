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

import "fmt"

// NodeStore abstracts the persistence layer for BMT nodes.
// Nodes are stored by (version, path) rather than content-addressed hash.
// Implementations must be safe for use from a single goroutine;
// callers are responsible for external synchronization if needed.
type NodeStore interface {
	// Get retrieves a node value by version and path.
	// Returns ErrNotFound if the node is not present.
	Get(version uint64, path Path) (NodeValue, error)

	// Put stores a node value at the given version and path.
	Put(version uint64, path Path, value NodeValue) error

	// Delete removes a node by version and path. Not an error if absent.
	Delete(version uint64, path Path) error
}

// MemStore is an in-memory NodeStore for testing.
type MemStore struct {
	nodes map[string]NodeValue
}

// NewMemStore creates a new empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{nodes: make(map[string]NodeValue)}
}

// makeKey creates a composite key from version and path.
func makeKey(version uint64, path Path) string {
	return fmt.Sprintf("%d:%s", version, path.String())
}

func (m *MemStore) Get(version uint64, path Path) (NodeValue, error) {
	key := makeKey(version, path)
	data, ok := m.nodes[key]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make(NodeValue, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *MemStore) Put(version uint64, path Path, value NodeValue) error {
	key := makeKey(version, path)
	cp := make(NodeValue, len(value))
	copy(cp, value)
	m.nodes[key] = cp
	return nil
}

func (m *MemStore) Delete(version uint64, path Path) error {
	key := makeKey(version, path)
	delete(m.nodes, key)
	return nil
}

// Len returns the number of nodes in the store.
func (m *MemStore) Len() int {
	return len(m.nodes)
}
