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

package lmdb

import (
	"context"
	"fmt"
	"sync"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/n42blockchain/N42/common/db"
)

type DBI struct {
	*mdbx.DBI
	env    *mdbx.Env
	ctx    context.Context
	cancel context.CancelFunc

	lock sync.RWMutex
	name string
}

func newDBI(ctx context.Context, env *mdbx.Env, name string) (*DBI, error) {
	var mdb mdbx.DBI
	err := env.Update(func(txn *mdbx.Txn) error {
		var txnErr error
		mdb, txnErr = txn.OpenDBI(name, mdbx.Create, nil, nil)
		return txnErr
	})
	if err != nil {
		return nil, err
	}

	c, cancel := context.WithCancel(ctx)
	return &DBI{
		DBI:    &mdb,
		env:    env,
		ctx:    c,
		cancel: cancel,
		name:   name,
	}, nil
}

func (db *DBI) Get(key []byte) (value []byte, err error) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	err = db.env.View(func(txn *mdbx.Txn) error {
		value, err = txn.Get(*db.DBI, key)
		return err
	})
	return value, err
}

func (db *DBI) Gets(key []byte, count uint) (keys [][]byte, values [][]byte, err error) {
	db.lock.RLock()
	defer db.lock.RUnlock()
	err = db.env.View(func(txn *mdbx.Txn) error {
		cur, err := txn.OpenCursor(*db.DBI)
		if err != nil {
			return err
		}
		defer cur.Close()

		k, v, err := cur.Get(key, nil, mdbx.Set)
		if err != nil {
			return err
		}

		keys = append(keys, k)
		values = append(values, v)
		for {
			k, v, err = cur.Get(nil, nil, mdbx.Next)
			if mdbx.IsNotFound(err) {
				break
			}
			if err != nil {
				return err
			}

			keys = append(keys, k)
			values = append(values, v)
			if len(keys) >= int(count) {
				break
			}
		}

		return nil
	})

	return keys, values, err
}

func (db *DBI) GetIterator(key []byte) (db.IIterator, error) {
	return newIterator(db, key)
}

func (db *DBI) Put(key []byte, value []byte) error {
	return db.env.Update(func(txn *mdbx.Txn) error {
		return txn.Put(*db.DBI, key, value, 0)
	})
}

func (db *DBI) Puts(keys [][]byte, values [][]byte) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	if len(keys) != len(values) {
		return fmt.Errorf("keys and values length mismatch: %d vs %d", len(keys), len(values))
	}

	return db.env.Update(func(txn *mdbx.Txn) error {
		for i := range keys {
			if err := txn.Put(*db.DBI, keys[i], values[i], 0); err != nil {
				return err
			}
		}
		return nil
	})
}

func (db *DBI) Drop() error {
	db.lock.Lock()
	defer db.lock.Unlock()

	return db.env.Update(func(txn *mdbx.Txn) error {
		return txn.Drop(*db.DBI, true)
	})
}

func (db *DBI) Delete(key []byte) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	return db.env.Update(func(txn *mdbx.Txn) error {
		return txn.Del(*db.DBI, key, nil)
	})
}
