// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// genesis_iter.go adapts MDBX Account/Storage cursors to the
// AccountIterator / StorageIterator types consumed by the V2 genesis
// changeset encoders in changeset_codec.go. Cursors already return rows
// in key-sorted order, so the iterators preserve the (address) /
// (address, slot) ordering the encoders require.

package ethel

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// newGenesisAccountIterator returns an AccountIterator that walks the
// Account table in address-sorted order, yielding each (address, raw v2
// bytes) pair to the encoder callback.
func newGenesisAccountIterator(tx kv.Tx) AccountIterator {
	return func(fn func(addr types.Address, v []byte) error) error {
		cursor, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer cursor.Close()
		for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
			if err != nil {
				return err
			}
			if len(k) != 20 {
				continue
			}
			var addr types.Address
			copy(addr[:], k)
			if err := fn(addr, v); err != nil {
				return err
			}
		}
		return nil
	}
}

// newGenesisStorageIterator returns a StorageIterator that walks the
// Storage table in (address, slot)-sorted order, yielding each triple
// to the encoder callback. Composite keys are addr(20) || slot(32) = 52
// bytes; shorter keys are skipped.
func newGenesisStorageIterator(tx kv.Tx) StorageIterator {
	return func(fn func(addr types.Address, slot types.Hash, v []byte) error) error {
		cursor, err := tx.Cursor(modules.Storage)
		if err != nil {
			return err
		}
		defer cursor.Close()
		for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
			if err != nil {
				return err
			}
			if len(k) != 52 {
				continue
			}
			var addr types.Address
			copy(addr[:], k[:20])
			var slot types.Hash
			copy(slot[:], k[20:52])
			if err := fn(addr, slot, v); err != nil {
				return err
			}
		}
		return nil
	}
}
