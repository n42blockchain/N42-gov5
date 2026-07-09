// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// WriteQMDBUndo persists one block's QMDB undo record (the sliding-window
// historical-proof data; see lib/qmdb/undo.go).
func WriteQMDBUndo(tx kv.Putter, blockNum uint64, undo *qmdb.BlockUndo) error {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], blockNum)
	return tx.Put(modules.QMDBUndoWindow, key[:], undo.Marshal())
}

// ReadQMDBUndos loads the consecutive undo records for blocks (target, head] —
// oldest first, exactly the shape qmdb.Tree.ProofAt expects. Returns nil (no
// error) if any block in the range is missing: the window does not reach back
// to the target.
func ReadQMDBUndos(tx kv.Getter, target, head uint64) ([]*qmdb.BlockUndo, error) {
	if target >= head {
		return nil, fmt.Errorf("qmdb undo: target %d not below head %d", target, head)
	}
	out := make([]*qmdb.BlockUndo, 0, head-target)
	for b := target + 1; b <= head; b++ {
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], b)
		v, err := tx.GetOne(modules.QMDBUndoWindow, key[:])
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, nil // window gap — target is out of reach
		}
		u, err := qmdb.UnmarshalBlockUndo(v)
		if err != nil {
			return nil, fmt.Errorf("qmdb undo block %d: %w", b, err)
		}
		out = append(out, u)
	}
	return out, nil
}

// qmdbAppliedKey tracks the head of the APPLIED chain — the block whose state
// the world state (PlainState + QMDB tree) currently reflects. Distinct from
// the canonical head: under HotStuff, canonicalization is commit-driven and
// lags, while speculative imports move the applied head immediately. The
// sibling-switch unwind reverts applied blocks until the applied head equals
// the incoming block's parent.
var qmdbAppliedKey = []byte("qmdbAppliedHead")

// WriteQMDBApplied records the applied-chain head (num || hash).
func WriteQMDBApplied(tx kv.Putter, blockNum uint64, hash [32]byte) error {
	v := make([]byte, 8+32)
	binary.BigEndian.PutUint64(v[:8], blockNum)
	copy(v[8:], hash[:])
	return tx.Put(modules.DatabaseInfo, qmdbAppliedKey, v)
}

// ReadQMDBApplied returns the applied-chain head. ok=false when the marker was
// never written (fresh replay data) — callers fall back to the stored head.
func ReadQMDBApplied(tx kv.Getter) (blockNum uint64, hash [32]byte, ok bool, err error) {
	v, err := tx.GetOne(modules.DatabaseInfo, qmdbAppliedKey)
	if err != nil || len(v) < 8+32 {
		return 0, hash, false, err
	}
	copy(hash[:], v[8:40])
	return binary.BigEndian.Uint64(v[:8]), hash, true, nil
}

// PruneQMDBUndoBelow deletes undo records for blocks below keepFrom, bounding
// the window's storage.
func PruneQMDBUndoBelow(tx kv.RwTx, keepFrom uint64) error {
	c, err := tx.RwCursor(modules.QMDBUndoWindow)
	if err != nil {
		return err
	}
	defer c.Close()
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			return err
		}
		if len(k) == 8 && binary.BigEndian.Uint64(k) >= keepFrom {
			break // keys are big-endian ordered; everything from here on stays
		}
		if err := c.DeleteCurrent(); err != nil {
			return err
		}
	}
	return nil
}
