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
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/kv"
)

// JMTNode is the MDBX table name for JMT nodes.
// Defined here so callers can reference it when registering the table.
const JMTNodeTable = "JMTNode"

// JMTRootTable stores the latest JMT root hash for recovery.
const JMTRootTable = "JMTRoot"

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
