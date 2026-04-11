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
// Transaction-scoped MDBX implementation of jmt.NodeStore. MDBXStore
// wraps a single kv.RwTx so every flush ties its writes to the enclosing
// block commit inside ChainDB.Update. JMTNodeTable holds serialized
// nodes keyed by their Blake3 hash, JMTRootTable persists the latest
// root + version for crash recovery, and the deprecated JMTVersionRoots
// constant is retained purely for backward compatibility with old
// databases that still have the table defined.

package store

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/kv"
)

// JMTNode is the MDBX table name for JMT nodes.
// Defined here so callers can reference it when registering the table.
const JMTNodeTable = "JMTNode"

// JMTRootTable stores the latest JMT root hash and version for recovery.
const JMTRootTable = "JMTRoot"

// JMTVersionRootsTable is DEPRECATED — state root lives in block header.
// Kept as constant for backward compatibility with existing databases.
const JMTVersionRootsTable = "JMTVersionRoots"

// MDBXStore implements jmt.NodeStore backed by an MDBX read-write transaction.
// Each instance wraps a single kv.RwTx — create a new MDBXStore per DB
// transaction (inside ChainDB.Update).
type MDBXStore struct {
	tx    kv.RwTx
	table string
}

// NewMDBXStore creates a NodeStore that reads/writes JMT nodes in the given
// MDBX transaction and table.
func NewMDBXStore(tx kv.RwTx, table string) *MDBXStore {
	return &MDBXStore{tx: tx, table: table}
}

func (s *MDBXStore) Get(hash jmt.Hash) ([]byte, error) {
	data, err := s.tx.GetOne(s.table, hash[:])
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, jmt.ErrNotFound
	}
	return data, nil
}

func (s *MDBXStore) Put(hash jmt.Hash, data []byte) error {
	return s.tx.Put(s.table, hash[:], data)
}

func (s *MDBXStore) Delete(hash jmt.Hash) error {
	return s.tx.Delete(s.table, hash[:])
}

func (s *MDBXStore) Has(hash jmt.Hash) (bool, error) {
	data, err := s.tx.GetOne(s.table, hash[:])
	if err != nil {
		return false, err
	}
	return data != nil, nil
}

// PutBatch writes multiple nodes in a single operation within the same
// MDBX transaction. More efficient than individual Put() calls when
// flushing many dirty nodes.
func (s *MDBXStore) PutBatch(entries map[jmt.Hash][]byte) error {
	for h, data := range entries {
		if err := s.tx.Put(s.table, h[:], data); err != nil {
			return err
		}
	}
	return nil
}

// Compile-time check: MDBXStore implements BatchNodeStore.
var _ jmt.BatchNodeStore = (*MDBXStore)(nil)

// ReadJMTRoot reads the latest persisted JMT root hash from the JMTRoot table.
func ReadJMTRoot(tx kv.Tx) (jmt.Hash, error) {
	data, err := tx.GetOne(JMTRootTable, []byte("root"))
	if err != nil {
		return jmt.EmptyHash, err
	}
	if data == nil || len(data) < jmt.HashSize {
		return jmt.EmptyHash, nil
	}
	var h jmt.Hash
	copy(h[:], data[:jmt.HashSize])
	return h, nil
}

// WriteJMTRoot persists the JMT root hash for crash recovery.
func WriteJMTRoot(tx kv.RwTx, root jmt.Hash) error {
	return tx.Put(JMTRootTable, []byte("root"), root[:])
}

// ReadJMTVersion reads the last persisted tree version (block height).
func ReadJMTVersion(tx kv.Tx) (uint64, error) {
	data, err := tx.GetOne(JMTRootTable, []byte("version"))
	if err != nil {
		return 0, err
	}
	if data == nil || len(data) < 8 {
		return 0, nil
	}
	return binary.BigEndian.Uint64(data), nil
}

// WriteJMTVersion persists the tree version (block height) for progress tracking.
func WriteJMTVersion(tx kv.RwTx, version uint64) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], version)
	return tx.Put(JMTRootTable, []byte("version"), buf[:])
}

// WriteJMTVersionRoot records the JMT root hash at a specific block height.
// This builds an index for historical state proof generation.
func WriteJMTVersionRoot(tx kv.RwTx, height uint64, root jmt.Hash) error {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], height)
	return tx.Put(JMTVersionRootsTable, key[:], root[:])
}

// ReadJMTVersionRoot retrieves the JMT root hash at an exact block height.
// Returns EmptyHash if no root is recorded for that height.
func ReadJMTVersionRoot(tx kv.Tx, height uint64) (jmt.Hash, error) {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], height)
	data, err := tx.GetOne(JMTVersionRootsTable, key[:])
	if err != nil {
		return jmt.EmptyHash, err
	}
	if data == nil || len(data) < jmt.HashSize {
		return jmt.EmptyHash, nil
	}
	var h jmt.Hash
	copy(h[:], data[:jmt.HashSize])
	return h, nil
}

// ReadJMTVersionRootAt retrieves the JMT root hash effective at any block height,
// including heights where no state change occurred. It finds the latest entry
// at or before the given height using a cursor seek.
// Returns (root, actualHeight, error). EmptyHash if no root exists at or before height.
func ReadJMTVersionRootAt(tx kv.Tx, height uint64) (root jmt.Hash, actualHeight uint64, err error) {
	cursor, err := tx.Cursor(JMTVersionRootsTable)
	if err != nil {
		return jmt.EmptyHash, 0, err
	}
	defer cursor.Close()

	// Seek to the target height. If exact match, use it.
	// If no exact match, MDBX cursor lands on the next key — go back one.
	var seekKey [8]byte
	binary.BigEndian.PutUint64(seekKey[:], height)

	k, v, err := cursor.Seek(seekKey[:])
	if err != nil {
		return jmt.EmptyHash, 0, err
	}

	if k == nil {
		// Target height past all entries — get the last (highest) entry.
		k, v, err = cursor.Last()
		if err != nil {
			return jmt.EmptyHash, 0, err
		}
	} else if len(k) == 8 && binary.BigEndian.Uint64(k) == height {
		// Exact match.
		if len(v) >= jmt.HashSize {
			copy(root[:], v[:jmt.HashSize])
			return root, height, nil
		}
	} else {
		// Seek landed on a key > target — go to previous entry.
		k, v, err = cursor.Prev()
		if err != nil {
			return jmt.EmptyHash, 0, err
		}
	}

	if k == nil || len(k) < 8 || len(v) < jmt.HashSize {
		return jmt.EmptyHash, 0, nil
	}
	actualHeight = binary.BigEndian.Uint64(k)
	copy(root[:], v[:jmt.HashSize])
	return root, actualHeight, nil
}
