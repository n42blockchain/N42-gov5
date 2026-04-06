// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// rethdb.go — shared utilities for reading Reth/Erigon MDBX tables.

package cscompact

import (
	"context"
	"encoding/binary"
	"sort"

	"github.com/n42blockchain/N42/lib/kv"
)

// TxBlockEntry maps a transaction number to its block number.
// Used for Reth's TransactionBlocks table (sparse: one entry per block boundary).
type TxBlockEntry struct {
	TxNum    uint64
	BlockNum uint64
}

// LoadTransactionBlocks reads the TransactionBlocks table into a sorted slice.
// Format: key=txNum(8B BE), value=blockNum(8B BE).
func LoadTransactionBlocks(db kv.RoDB, ctx context.Context) ([]TxBlockEntry, error) {
	tx, err := db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor("TransactionBlocks")
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var entries []TxBlockEntry
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) < 8 || len(v) < 8 {
			continue
		}
		entries = append(entries, TxBlockEntry{
			TxNum:    binary.BigEndian.Uint64(k),
			BlockNum: binary.BigEndian.Uint64(v),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].TxNum < entries[j].TxNum
	})
	return entries, nil
}

// TxNumToBlockNum finds the block number for a given txNum using binary search.
func TxNumToBlockNum(entries []TxBlockEntry, txNum uint64) uint64 {
	idx := sort.Search(len(entries), func(i int) bool {
		return entries[i].TxNum > txNum
	}) - 1
	if idx < 0 {
		return 0
	}
	return entries[idx].BlockNum
}

// DetectBlockNum reads a BE-encoded block number from a key.
// Both Erigon and Reth use Big-Endian encoding for MDBX keys.
func DetectBlockNum(k []byte) uint64 {
	if len(k) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(k[:8])
}
