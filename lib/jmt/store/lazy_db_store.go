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
	"context"

	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/kv"
)

// LazyDBStore implements jmt.NodeStore by reading from an MDBX database
// on demand. Each Get/Has call opens a short-lived read-only transaction.
//
// Writes are no-ops because the JMT tree buffers mutations in its dirty map
// and flushes them via FlushTo(MDBXStore) inside the block-commit transaction.
//
// This store is used at node startup so the JMT tree can load previously
// persisted nodes from MDBX without holding a long-lived transaction.
type LazyDBStore struct {
	db    kv.RoDB
	table string
	ctx   context.Context
}

// NewLazyDBStore creates a read-through NodeStore backed by the given database.
func NewLazyDBStore(ctx context.Context, db kv.RoDB, table string) *LazyDBStore {
	return &LazyDBStore{db: db, table: table, ctx: ctx}
}

func (s *LazyDBStore) Get(hash jmt.Hash) ([]byte, error) {
	tx, err := s.db.BeginRo(s.ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	data, err := tx.GetOne(s.table, hash[:])
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, jmt.ErrNotFound
	}
	// Copy data out of the MDBX transaction buffer.
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (s *LazyDBStore) Put(jmt.Hash, []byte) error {
	// Writes go through tree.FlushTo(MDBXStore) inside the block transaction.
	return nil
}

func (s *LazyDBStore) Delete(jmt.Hash) error {
	// Deletes go through tree.FlushTo(MDBXStore) inside the block transaction.
	return nil
}

func (s *LazyDBStore) Has(hash jmt.Hash) (bool, error) {
	tx, err := s.db.BeginRo(s.ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	data, err := tx.GetOne(s.table, hash[:])
	if err != nil {
		return false, err
	}
	return data != nil, nil
}

// Compile-time check.
var _ jmt.NodeStore = (*LazyDBStore)(nil)
