// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// head_marker.go — the eth-el head marker.
//
// A snapshot-direct or hashed-canonical datadir never writes the classic head
// records (HeadBlockHash / CurrentBlockNumber): the eldevp2p downloader and
// ethexec's replay both track the head in SyncStageProgress under this key.
// Any reader that answers "what block are we at" has to consult it, or it
// reports 0 for a node that is fully caught up and following the tip.

package ethel

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/lib/kv"
)

// HeadMarkerKey is the SyncStageProgress key holding the eth-el head.
var HeadMarkerKey = []byte("ethel-last-block")

// ReadHeadMarker returns the head recorded by the eldevp2p downloader / replay,
// and whether the marker exists.
func ReadHeadMarker(tx kv.Tx) (uint64, bool) {
	v, err := tx.GetOne(kv.SyncStageProgress, HeadMarkerKey)
	if err != nil || len(v) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(v), true
}

// WriteHeadMarker records the canonical execution head shared by Engine API,
// devp2p sync, and public RPC readers.
func WriteHeadMarker(tx kv.Putter, head uint64) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], head)
	return tx.Put(kv.SyncStageProgress, HeadMarkerKey, encoded[:])
}
