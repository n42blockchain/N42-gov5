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
	// BMTNodeTable stores content-addressed BMT nodes.
	// key: blake3_hash(32B)   value: node_data (65B internal / 33+B leaf)
	BMTNodeTable = "BMTNode"

	// BMTRootTable stores the latest BMT root hash for crash recovery.
	BMTRootTable = "BMTRoot"

)

// MDBXStore implements bmt.NodeStore as a content-addressed store.
// Each node is keyed by its Blake3 hash — simple GetOne, no cursor seek.
type MDBXStore struct {
	tx    kv.RwTx
	table string
}

func NewMDBXStore(tx kv.RwTx, table string) *MDBXStore {
	return &MDBXStore{tx: tx, table: table}
}

func (s *MDBXStore) Get(hash bmt.Hash) (bmt.NodeValue, error) {
	data, err := s.tx.GetOne(s.table, hash[:])
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

func (s *MDBXStore) Put(hash bmt.Hash, value bmt.NodeValue) error {
	return s.tx.Put(s.table, hash[:], value)
}

// Compile-time check.
var _ bmt.NodeStore = (*MDBXStore)(nil)

// --- Recovery & version-root helpers (separate tables) ---

func WriteBMTRoot(tx kv.RwTx, root bmt.Hash) error {
	return tx.Put(BMTRootTable, []byte("root"), root[:])
}

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

func WriteBMTVersion(tx kv.RwTx, version uint64) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], version)
	return tx.Put(BMTRootTable, []byte("version"), buf[:])
}

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

