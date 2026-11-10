// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"os"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Commit-to-canonical keeps block reads off the MDBX writer (r76a).
//
// A commit whose block is no longer in the block cache read and decoded the
// whole block inside its write transaction. E1N26 run 46 (node0, 326,000-tx
// frame blocks): a second, trailing stream of commits -- late duplicates of
// blocks already canonical, 50-250 blocks behind the head -- did that for
// every block, 360-480 ms each under the single writer, only to return at
// the monotonicity guard. Every other block write then waited ~450 ms to
// begin (beginBehind = CommitToCanonicalWith) and the fleet fell to two
// blocks per half second.
//
// With N42_COMMIT_DUP_FAST=1 a commit that misses the block cache first
// looks on a read transaction: a block that is already canonical at or below
// the committed head, with no own-unverified mark left to clear, and whose
// transaction lookups the tail indexer owns, needs no write at all. Any
// other block is read there and handed to the write transaction, which then
// runs unchanged.
var commitDupFast = os.Getenv("N42_COMMIT_DUP_FAST") == "1"

// SetCommitDupFastForTest switches the fast path and returns the restore.
func SetCommitDupFastForTest(on bool) func() {
	prev := commitDupFast
	commitDupFast = on
	return func() { commitDupFast = prev }
}

// commitAfterPreReadForTest, when set, runs between the pre-read and the
// write transaction (tests: the block's own write landing in that gap).
var commitAfterPreReadForTest func()

// commitPreRead reports whether the commit of hash is a duplicate that needs
// no write; otherwise it returns the block read from the database (nil when
// it is not stored, which the write transaction reports as before).
func (bc *BlockChain) commitPreRead(hash types.Hash) (dup bool, number uint64, blk *block.Block, err error) {
	err = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		if bc.txIndexer != nil {
			if n := rawdb.ReadHeaderNumber(tx, hash); n != nil {
				if hn := rawdb.ReadHeaderNumber(tx, rawdb.ReadHeadBlockHash(tx)); hn != nil && *hn >= *n {
					existing, rerr := rawdb.ReadCanonicalHash(tx, *n)
					if rerr != nil {
						return rerr
					}
					if existing == hash {
						if !rawdb.IsOwnUnverifiedMarked(tx, hash) {
							dup, number = true, *n
							return nil
						}
					}
				}
			}
		}
		var rerr error
		blk, rerr = rawdb.ReadBlockByHash(tx, hash)
		return rerr
	})
	return dup, number, blk, err
}
