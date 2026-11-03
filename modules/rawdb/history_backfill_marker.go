// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// historyIndexedKey marks how far the history inverted index has been built
// when index maintenance runs off the block-commit path.
//
// It is the interlock's source of truth: historical queries must wait until
// this marker covers the snapshot head. A query below the marker can still
// need a later change in the unindexed tail; falling back to current plain
// state in that case would silently return the wrong value.
var historyIndexedKey = []byte("historyIndexedThrough")

// WriteHistoryIndexedThrough records the highest block whose changesets have
// been folded into the history index. Written in the SAME transaction as the
// index rows it describes, so a crash can leave the marker behind the index
// (harmless: the range is rebuilt) but never ahead of it (which would let a
// query read a gap as "untouched").
func WriteHistoryIndexedThrough(tx kv.Putter, blockNum uint64) error {
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], blockNum)
	return tx.Put(modules.DatabaseInfo, historyIndexedKey, v[:])
}

// ReadHistoryIndexedThrough returns the marker, ok=false when it was never
// written. A missing marker means nothing has been backfilled, which callers
// must treat as "index covers nothing" rather than "index covers everything".
func ReadHistoryIndexedThrough(tx kv.Getter) (uint64, bool, error) {
	v, err := tx.GetOne(modules.DatabaseInfo, historyIndexedKey)
	if err != nil {
		return 0, false, err
	}
	if len(v) != 8 {
		return 0, false, nil
	}
	return binary.BigEndian.Uint64(v), true, nil
}

// historyUnwindGenerationKey counts changeset unwinds. A fold reads changesets
// outside its write transaction, so it compares this counter to detect that
// the rows it prepared were replaced. An absent key reads as zero.
var historyUnwindGenerationKey = []byte("historyUnwindGeneration")

// ReadHistoryUnwindGeneration returns the number of changeset unwinds recorded
// so far.
func ReadHistoryUnwindGeneration(tx kv.Getter) (uint64, error) {
	v, err := tx.GetOne(modules.DatabaseInfo, historyUnwindGenerationKey)
	if err != nil || len(v) != 8 {
		return 0, err
	}
	return binary.BigEndian.Uint64(v), nil
}

// NoteHistoryUnwind records, in the unwinding transaction, that block n's
// changesets are being removed. A marker at or above n would claim a block the
// index has not seen once a replacement lands there, so it moves to n-1 and the
// backfiller folds the replacement. The generation invalidates any fold in
// flight, including one that started below n.
func NoteHistoryUnwind(tx kv.RwTx, n uint64) error {
	marker, ok, err := ReadHistoryIndexedThrough(tx)
	if err != nil {
		return err
	}
	if ok && n > 0 && marker >= n {
		if err := WriteHistoryIndexedThrough(tx, n-1); err != nil {
			return err
		}
	}
	generation, err := ReadHistoryUnwindGeneration(tx)
	if err != nil {
		return err
	}
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], generation+1)
	return tx.Put(modules.DatabaseInfo, historyUnwindGenerationKey, v[:])
}
