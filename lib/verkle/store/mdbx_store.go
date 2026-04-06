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

	libverkle "github.com/n42blockchain/N42/lib/verkle"
	"github.com/n42blockchain/N42/lib/kv"
)

const (
	// VerkleNodeTable stores content-addressed Verkle nodes.
	// key: commitment_bytes(64B)   value: serialized_node (97B internal / 288B+ leaf)
	VerkleNodeTable = "VerkleNode"

	// VerkleRootTable stores the latest Verkle root commitment and version
	// for crash recovery.
	VerkleRootTable = "VerkleRoot"
)

// MDBXStore implements verkle.NodeStore backed by an MDBX read-write transaction.
type MDBXStore struct {
	tx    kv.RwTx
	table string
}

func NewMDBXStore(tx kv.RwTx, table string) *MDBXStore {
	return &MDBXStore{tx: tx, table: table}
}

func (s *MDBXStore) Get(key libverkle.CommitmentKey) ([]byte, error) {
	data, err := s.tx.GetOne(s.table, key[:])
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, libverkle.ErrNotFound
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (s *MDBXStore) Put(key libverkle.CommitmentKey, data []byte) error {
	return s.tx.Put(s.table, key[:], data)
}

func (s *MDBXStore) Has(key libverkle.CommitmentKey) (bool, error) {
	data, err := s.tx.GetOne(s.table, key[:])
	if err != nil {
		return false, err
	}
	return data != nil, nil
}

// PutBatch writes multiple nodes in a single pass, sorted for MDBX
// cursor efficiency.
func (s *MDBXStore) PutBatch(entries map[libverkle.CommitmentKey][]byte) error {
	for key, data := range entries {
		if err := s.tx.Put(s.table, key[:], data); err != nil {
			return err
		}
	}
	return nil
}

// Compile-time checks.
var (
	_ libverkle.NodeStore      = (*MDBXStore)(nil)
	_ libverkle.BatchNodeStore = (*MDBXStore)(nil)
)

// --- Recovery & version helpers (VerkleRootTable) ---

// WriteVerkleRoot persists the latest root commitment for crash recovery.
func WriteVerkleRoot(tx kv.RwTx, commitment libverkle.CommitmentKey) error {
	return tx.Put(VerkleRootTable, []byte("root"), commitment[:])
}

// ReadVerkleRoot retrieves the latest persisted root commitment.
func ReadVerkleRoot(tx kv.Tx) (libverkle.CommitmentKey, error) {
	data, err := tx.GetOne(VerkleRootTable, []byte("root"))
	if err != nil {
		return libverkle.CommitmentKey{}, err
	}
	if data == nil || len(data) < 64 {
		return libverkle.CommitmentKey{}, nil
	}
	var key libverkle.CommitmentKey
	copy(key[:], data[:64])
	return key, nil
}

// WriteVerkleVersion persists the latest flushed block number.
func WriteVerkleVersion(tx kv.RwTx, version uint64) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], version)
	return tx.Put(VerkleRootTable, []byte("version"), buf[:])
}

// ReadVerkleVersion retrieves the latest flushed block number.
func ReadVerkleVersion(tx kv.Tx) (uint64, error) {
	data, err := tx.GetOne(VerkleRootTable, []byte("version"))
	if err != nil {
		return 0, err
	}
	if data == nil || len(data) < 8 {
		return 0, nil
	}
	return binary.BigEndian.Uint64(data), nil
}
