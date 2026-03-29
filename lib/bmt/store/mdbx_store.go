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

package store

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/lib/bmt"
	"github.com/n42blockchain/N42/lib/kv"
)

const (
	// BMTNodeTable is the MDBX table name for BMT nodes.
	// key: version(8B) + bitlen(2B) + packed_path(var)
	// value: hash(32B) for internal nodes, inline data(<32B) for leaves
	BMTNodeTable = "BMTNode"

	// BMTRootTable stores the latest BMT root hash and version for recovery.
	BMTRootTable = "BMTRoot"

	// BMTVersionRootsTable maps block height to BMT root hash for historical proofs.
	BMTVersionRootsTable = "BMTVersionRoots"
)

// MDBXStore implements bmt.NodeStore backed by an MDBX read-write transaction.
// Each instance wraps a single kv.RwTx -- create a new MDBXStore per DB
// transaction (inside ChainDB.Update).
type MDBXStore struct {
	tx    kv.RwTx
	table string
}

// NewMDBXStore creates a NodeStore that reads/writes BMT nodes in the given
// MDBX transaction and table.
func NewMDBXStore(tx kv.RwTx, table string) *MDBXStore {
	return &MDBXStore{tx: tx, table: table}
}

// Get retrieves a serialized BMT node by version and path.
func (s *MDBXStore) Get(version uint64, path bmt.Path) (bmt.NodeValue, error) {
	key := path.EncodeMDBXKey(version)
	data, err := s.tx.GetOne(s.table, key)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, bmt.ErrNotFound
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

// Put stores a serialized BMT node at the given version and path.
func (s *MDBXStore) Put(version uint64, path bmt.Path, value bmt.NodeValue) error {
	key := path.EncodeMDBXKey(version)
	return s.tx.Put(s.table, key, value)
}

// Delete removes a BMT node by version and path.
func (s *MDBXStore) Delete(version uint64, path bmt.Path) error {
	key := path.EncodeMDBXKey(version)
	return s.tx.Delete(s.table, key)
}

// Compile-time check: MDBXStore implements bmt.NodeStore.
var _ bmt.NodeStore = (*MDBXStore)(nil)

// WriteBMTRoot persists the latest BMT root hash for crash recovery.
func WriteBMTRoot(tx kv.RwTx, root bmt.Hash) error {
	return tx.Put(BMTRootTable, []byte("root"), root[:])
}

// ReadBMTRoot reads the latest persisted BMT root hash.
// Returns EmptyHash if no root has been written yet.
func ReadBMTRoot(tx kv.Tx) (bmt.Hash, error) {
	data, err := tx.GetOne(BMTRootTable, []byte("root"))
	if err != nil {
		return bmt.EmptyHash, err
	}
	if data == nil || len(data) < bmt.HashSize {
		return bmt.EmptyHash, nil
	}
	var h bmt.Hash
	copy(h[:], data[:bmt.HashSize])
	return h, nil
}

// WriteBMTVersion persists the tree version (block height) for progress tracking.
func WriteBMTVersion(tx kv.RwTx, version uint64) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], version)
	return tx.Put(BMTRootTable, []byte("version"), buf[:])
}

// ReadBMTVersion reads the last persisted tree version (block height).
func ReadBMTVersion(tx kv.Tx) (uint64, error) {
	data, err := tx.GetOne(BMTRootTable, []byte("version"))
	if err != nil {
		return 0, err
	}
	if data == nil || len(data) < 8 {
		return 0, nil
	}
	return binary.BigEndian.Uint64(data), nil
}

// WriteBMTVersionRoot records the BMT root hash at a specific block height.
// This builds an index for historical state proof generation.
func WriteBMTVersionRoot(tx kv.RwTx, height uint64, root bmt.Hash) error {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], height)
	return tx.Put(BMTVersionRootsTable, key[:], root[:])
}

// ReadBMTVersionRoot retrieves the BMT root hash at an exact block height.
// Returns EmptyHash if no root is recorded for that height.
func ReadBMTVersionRoot(tx kv.Tx, height uint64) (bmt.Hash, error) {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], height)
	data, err := tx.GetOne(BMTVersionRootsTable, key[:])
	if err != nil {
		return bmt.EmptyHash, err
	}
	if data == nil || len(data) < bmt.HashSize {
		return bmt.EmptyHash, nil
	}
	var h bmt.Hash
	copy(h[:], data[:bmt.HashSize])
	return h, nil
}
