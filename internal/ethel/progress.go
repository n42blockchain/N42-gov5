// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/lib/kv"
)

const progressKey = "ethel-last-block"

// ReadProgress returns the last successfully committed block number.
// Returns 0 if no progress has been saved.
func ReadProgress(tx kv.Tx) uint64 {
	v, err := tx.GetOne(kv.SyncStageProgress, []byte(progressKey))
	if err != nil || len(v) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(v)
}

// WriteProgress saves the last successfully committed block number.
func WriteProgress(tx kv.RwTx, blockNum uint64) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], blockNum)
	return tx.Put(kv.SyncStageProgress, []byte(progressKey), buf[:])
}
