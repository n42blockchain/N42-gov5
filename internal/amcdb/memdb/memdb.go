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

package memdb

import (
	"errors"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/db"
)

type MemoryDB struct {
	db   map[string][]byte
	lock sync.RWMutex
}

var errSnapshotNotSupported = errors.New("snapshot not implemented for MemoryDB")

func NewMemDB() db.IDatabase {
	return &MemoryDB{
		db: make(map[string][]byte),
	}
}

func (m *MemoryDB) OpenReader(dbName string) (db.IDatabaseReader, error) {
	return m, nil
}

func (m *MemoryDB) OpenWriter(dbName string) (db.IDatabaseWriter, error) {
	return nil, nil
}

func (m *MemoryDB) Open(dbName string) (db.IDatabaseWriterReader, error) {
	return nil, nil
}

func (m *MemoryDB) Snapshot() (db.ISnapshot, error) {
	return nil, errSnapshotNotSupported
}

func (m *MemoryDB) Close() error {
	return nil
}

func (m *MemoryDB) Get(key []byte) ([]byte, error) {
	m.lock.RLock()
	defer m.lock.RUnlock()

	if v, ok := m.db[string(key)]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("key not found")
}

func (m *MemoryDB) Gets(key []byte, count uint) ([][]byte, [][]byte, error) {
	return nil, nil, nil
}

func (m *MemoryDB) GetIterator(key []byte) (db.IIterator, error) {
	return nil, nil
}

func (m *MemoryDB) Put(key []byte, value []byte) error {
	m.lock.Lock()
	defer m.lock.Unlock()

	k := string(key)
	if _, ok := m.db[k]; ok {
		return fmt.Errorf("key already exists")
	}
	m.db[k] = value
	return nil
}

func (m *MemoryDB) Puts(keys [][]byte, values [][]byte) error {
	for i := range keys {
		if err := m.Put(keys[i], values[i]); err != nil {
			return err
		}
	}
	return nil
}

func (m *MemoryDB) Delete(key []byte) error {
	m.lock.Lock()
	defer m.lock.Unlock()

	k := string(key)
	if _, ok := m.db[k]; !ok {
		return fmt.Errorf("key not found")
	}
	delete(m.db, k)
	return nil
}

func (m *MemoryDB) Drop() error {
	return nil
}
