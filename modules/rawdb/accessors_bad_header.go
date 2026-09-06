// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// WriteBadHeaderMark records that the block with this hash failed validation
// on import. The mark stops a leader from re-proposing the stored sibling
// (BlockChain.LowestSiblingAtHeight); it does not affect import.
//
// Round 26 found the hole: two candidates built on stale reads were stored as
// siblings at head+1, and every later leader converged on the lowest hash of
// them, re-proposed it, and locked the fleet on a block nobody could apply.
func WriteBadHeaderMark(db kv.Putter, hash types.Hash, number uint64) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], number)
	return db.Put(modules.BadHeaderNumber, hash[:], buf[:])
}

// IsBadHeaderMarked reports whether WriteBadHeaderMark was recorded for hash.
func IsBadHeaderMarked(db kv.Getter, hash types.Hash) bool {
	v, err := db.GetOne(modules.BadHeaderNumber, hash[:])
	return err == nil && len(v) == 8
}
