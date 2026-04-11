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
//
// NodeStore interface and MemStore reference implementation for the BMT.
// Nodes are content-addressed by their Blake3 hash so unchanged subtrees
// are automatically deduplicated between tree versions. The interface
// exposes Get/Put; MemStore keeps nodes in a map[Hash]NodeValue with
// defensive copies on both sides and is used by unit tests and tools
// that need an isolated in-process store.

package bmt

// NodeStore is a content-addressed store for BMT nodes.
// Nodes are keyed by their Blake3 hash — no version or path in the key.
// Structural sharing between tree versions happens naturally:
// unchanged subtrees share the same hash and the same store entry.
type NodeStore interface {
	Get(hash Hash) (NodeValue, error)
	Put(hash Hash, value NodeValue) error
}

// MemStore is an in-memory content-addressed NodeStore for testing.
type MemStore struct {
	nodes map[Hash]NodeValue
}

func NewMemStore() *MemStore {
	return &MemStore{nodes: make(map[Hash]NodeValue)}
}

func (m *MemStore) Get(hash Hash) (NodeValue, error) {
	data, ok := m.nodes[hash]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make(NodeValue, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *MemStore) Put(hash Hash, value NodeValue) error {
	cp := make(NodeValue, len(value))
	copy(cp, value)
	m.nodes[hash] = cp
	return nil
}

func (m *MemStore) Len() int { return len(m.nodes) }

// TotalBytes returns total storage used by all nodes (key + value).
func (m *MemStore) TotalBytes() int64 {
	var total int64
	for _, v := range m.nodes {
		total += int64(32 + len(v)) // Hash(32) + value
	}
	return total
}
