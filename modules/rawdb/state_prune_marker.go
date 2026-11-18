// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// statePrunedBelowKey (DatabaseInfo) records the pruner's horizon: changesets
// and history-index shards of blocks below it may be gone, so state as of a
// height below it cannot be reconstructed.
var statePrunedBelowKey = []byte("statePrunedBelow")

// statePrunedBelow mirrors the persisted horizon for CheckHistoricalState
// (which has no transaction). Loaded at startup, raised by the pruner.
var statePrunedBelow atomic.Uint64

// ReadStatePrunedBelow returns the persisted prune horizon (ok=false: never pruned).
func ReadStatePrunedBelow(tx kv.Getter) (uint64, bool, error) {
	v, err := tx.GetOne(modules.DatabaseInfo, statePrunedBelowKey)
	if err != nil || len(v) == 0 {
		return 0, false, err
	}
	if len(v) != 8 {
		return 0, false, fmt.Errorf("invalid statePrunedBelow marker length %d", len(v))
	}
	return binary.BigEndian.Uint64(v), true, nil
}

// RaiseStatePrunedBelow persists max(current, block) as the prune horizon and
// publishes it to CheckHistoricalState. Call in the tx that starts deleting
// below `block`, BEFORE the deletes, so readers are refused first.
func RaiseStatePrunedBelow(tx kv.RwTx, block uint64) error {
	cur, _, err := ReadStatePrunedBelow(tx)
	if err != nil {
		return err
	}
	if block <= cur {
		return nil
	}
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], block)
	if err := tx.Put(modules.DatabaseInfo, statePrunedBelowKey, v[:]); err != nil {
		return err
	}
	SetStatePrunedBelow(block)
	return nil
}

// SetStatePrunedBelow publishes the horizon in memory (startup load, tests).
// It only ever rises.
func SetStatePrunedBelow(block uint64) {
	for {
		cur := statePrunedBelow.Load()
		if block <= cur || statePrunedBelow.CompareAndSwap(cur, block) {
			return
		}
	}
}

// StatePrunedBelow returns the in-memory prune horizon (0 = nothing pruned).
func StatePrunedBelow() uint64 { return statePrunedBelow.Load() }

// ResetStatePrunedBelowForTesting clears the in-memory horizon (tests only:
// the horizon is process-global and only ever rises otherwise).
func ResetStatePrunedBelowForTesting() { statePrunedBelow.Store(0) }
