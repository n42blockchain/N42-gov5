// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// progress.go — last-committed-block marker stored in MDBX.
//
// ReadProgress and WriteProgress persist the block number of the last
// successfully committed batch under a single fixed key so that the
// executor can resume after a restart without scanning PlainState. The
// value is written inside the same MDBX transaction as the state batch
// it describes, which makes resume atomic with the freezer commit
// protocol in output_batcher.go.

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
