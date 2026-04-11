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
// Verkle tree persistence abstraction. CommitmentKey is the 64-byte
// uncompressed Banderwagon commitment used as a content-addressed key,
// and ErrNotFound signals a missing node. NodeStore exposes Get / Put /
// Has over those keys, while BatchNodeStore adds PutBatch for efficient
// MDBX cursor inserts. Flush walks an in-memory go-verkle root, uses
// BatchSerialize to convert projective coordinates to affine in one
// shot, then streams the resulting nodes into the target store.

package verkle

import (
	"errors"

	gverkle "github.com/ethereum/go-verkle"
)

// ErrNotFound is returned when a node is not present in the store.
var ErrNotFound = errors.New("verkle: node not found")

// CommitmentKey is the 64-byte uncompressed Banderwagon commitment used as
// the content-addressed key for Verkle nodes.
type CommitmentKey = [64]byte

// NodeStore abstracts the persistence layer for Verkle nodes.
// Nodes are keyed by their 64-byte uncompressed commitment bytes
// (content-addressed, like JMT/BMT).
type NodeStore interface {
	// Get retrieves a serialized Verkle node by its commitment key.
	// Returns ErrNotFound if absent.
	Get(key CommitmentKey) ([]byte, error)

	// Put stores a serialized Verkle node under its commitment key.
	Put(key CommitmentKey, data []byte) error

	// Has returns true if the commitment key exists in the store.
	Has(key CommitmentKey) (bool, error)
}

// BatchNodeStore extends NodeStore with batch write support for
// efficient MDBX cursor inserts.
type BatchNodeStore interface {
	NodeStore
	PutBatch(entries map[CommitmentKey][]byte) error
}

// Flush serializes all non-hashed nodes from the in-memory Verkle tree
// and writes them to the target store. It uses BatchSerialize for a
// single-shot projective→affine transformation (much faster than
// serializing nodes individually).
//
// Returns the number of new nodes written and total bytes.
func Flush(root gverkle.VerkleNode, target NodeStore) (nodes int, bytes int64, err error) {
	internal, ok := root.(*gverkle.InternalNode)
	if !ok {
		return 0, 0, errors.New("verkle: root is not an InternalNode")
	}

	serialized, err := internal.BatchSerialize()
	if err != nil {
		return 0, 0, err
	}

	// If target supports batch writes, collect and flush in one call.
	if batch, ok := target.(BatchNodeStore); ok {
		entries := make(map[CommitmentKey][]byte, len(serialized))
		for _, sn := range serialized {
			entries[sn.CommitmentBytes] = sn.SerializedBytes
		}
		if err := batch.PutBatch(entries); err != nil {
			return 0, 0, err
		}
		for _, sn := range serialized {
			bytes += int64(len(sn.SerializedBytes))
		}
		return len(serialized), bytes, nil
	}

	// Fallback: individual puts.
	for _, sn := range serialized {
		if err := target.Put(sn.CommitmentBytes, sn.SerializedBytes); err != nil {
			return nodes, bytes, err
		}
		nodes++
		bytes += int64(len(sn.SerializedBytes))
	}
	return nodes, bytes, nil
}

// Resolver returns a NodeResolverFn that loads serialized nodes from
// the given store. This is the bridge between go-verkle's lazy loading
// and our persistence layer.
func Resolver(store NodeStore) gverkle.NodeResolverFn {
	return func(path []byte) ([]byte, error) {
		if len(path) < 64 {
			return nil, ErrNotFound
		}
		var key CommitmentKey
		copy(key[:], path[:64])
		data, err := store.Get(key)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
}

// MemStore is an in-memory NodeStore for testing.
type MemStore struct {
	nodes map[CommitmentKey][]byte
}

func NewMemStore() *MemStore {
	return &MemStore{nodes: make(map[CommitmentKey][]byte)}
}

func (m *MemStore) Get(key CommitmentKey) ([]byte, error) {
	data, ok := m.nodes[key]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *MemStore) Put(key CommitmentKey, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	m.nodes[key] = cp
	return nil
}

func (m *MemStore) Has(key CommitmentKey) (bool, error) {
	_, ok := m.nodes[key]
	return ok, nil
}

func (m *MemStore) Len() int {
	return len(m.nodes)
}
