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

// ownUnverifiedMarkLen distinguishes an own-unverified mark (number + one
// flag byte) from a bad-header mark (number only) in the same table.
const ownUnverifiedMarkLen = 9

// WriteOwnUnverifiedMark records that this node sealed and wrote the block
// itself, and no follower has vouched for it yet. Like a bad-header mark it
// keeps LowestSiblingAtHeight from converging on the block; unlike one it is
// cleared by ClearOwnUnverifiedMark when the block is committed, and it does
// not make the node refuse its own deterministic rebuild (BadSibling).
//
// Rounds 35zq and 35zw: a leader's own build that every follower rejected
// stayed in its store as a same-height sibling, and after a restart the
// convergence rule re-proposed it -- the builder never verifies what it
// builds, so nothing had marked it bad on that node.
func WriteOwnUnverifiedMark(db kv.Putter, hash types.Hash, number uint64) error {
	var buf [ownUnverifiedMarkLen]byte
	binary.BigEndian.PutUint64(buf[:8], number)
	buf[8] = 1
	return db.Put(modules.BadHeaderNumber, hash[:], buf[:])
}

// IsOwnUnverifiedMarked reports whether WriteOwnUnverifiedMark stands for hash.
func IsOwnUnverifiedMarked(db kv.Getter, hash types.Hash) bool {
	v, err := db.GetOne(modules.BadHeaderNumber, hash[:])
	return err == nil && len(v) == ownUnverifiedMarkLen
}

// ClearOwnUnverifiedMark removes an own-unverified mark; a bad-header mark on
// the same hash is left alone.
func ClearOwnUnverifiedMark(db kv.RwTx, hash types.Hash) error {
	v, err := db.GetOne(modules.BadHeaderNumber, hash[:])
	if err != nil || len(v) != ownUnverifiedMarkLen {
		return err
	}
	return db.Delete(modules.BadHeaderNumber, hash[:])
}
