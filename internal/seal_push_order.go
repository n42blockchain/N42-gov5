// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Pre-check for the push-before-write ordering experiment. See
// docs/QS_BLOCK_TIME_BUDGET.md section 5 for why the ordering matters: the
// leader currently commits a sealed block to MDBX (206.8 ms on a full 22,857-tx
// block) and only then direct-pushes it, so every follower's 449 ms import
// starts after that write instead of alongside it.

package internal

import (
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
)

// CheckSealParentApplied reports whether a freshly sealed leader block would
// still pass the stale-seal gate in the write path. It runs the SAME predicate
// as writeBlockWithState (checkQMDBLeaderSealParent) against a read
// transaction, so a caller can learn the answer WITHOUT taking the write lock.
//
// The point is ordering, not a new rule. Pushing a sealed block before writing
// it lets followers import while the leader commits, but a block that the write
// path is about to reject as ErrStaleSeal must not be broadcast first: today
// such a block is dropped silently and nothing reaches the network, and that
// property has to survive the reordering. Running the identical predicate here
// keeps the two decisions from drifting apart.
//
// A nil error means "the write path's stale-seal gate would pass right now".
// It is a check against a snapshot, so the write can still lose a race it won
// here; that residue is a rare wasted follower import, not a safety loss — an
// unproposed block never collects a QC.
func (bc *BlockChain) CheckSealParentApplied(blk block.IBlock) error {
	if !bc.qmdbEnabled || blk == nil {
		return nil
	}
	number := blk.Number64().Uint64()
	parent := blk.ParentHash()
	return bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		return checkQMDBLeaderSealParent(tx, number, parent)
	})
}
