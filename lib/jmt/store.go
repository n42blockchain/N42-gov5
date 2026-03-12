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

// NodeStore abstracts the persistence layer for JMT nodes.
// Implementations must be safe for use from a single goroutine;
// callers are responsible for external synchronization if needed.
type NodeStore interface {
	// Get retrieves a serialized node by its content-addressed hash.
	// Returns ErrNotFound if the hash is not present.
	Get(hash Hash) ([]byte, error)

	// Put stores a serialized node under its content-addressed hash.
	Put(hash Hash, data []byte) error

	// Delete removes a node by hash. Not an error if absent.
	Delete(hash Hash) error

	// Has returns true if the hash exists in the store.
	Has(hash Hash) (bool, error)
}

// MemStore is an in-memory NodeStore for testing.
type MemStore struct {
	nodes map[Hash][]byte
}

// NewMemStore creates a new empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{nodes: make(map[Hash][]byte)}
}

func (m *MemStore) Get(hash Hash) ([]byte, error) {
	data, ok := m.nodes[hash]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *MemStore) Put(hash Hash, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	m.nodes[hash] = cp
	return nil
}

func (m *MemStore) Delete(hash Hash) error {
	delete(m.nodes, hash)
	return nil
}

func (m *MemStore) Has(hash Hash) (bool, error) {
	_, ok := m.nodes[hash]
	return ok, nil
}

// Len returns the number of nodes in the store.
func (m *MemStore) Len() int {
	return len(m.nodes)
}
