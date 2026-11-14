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
//
// Applied-head queries and the read-through cache reset.

package internal

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// AppliedHeadIs reports whether the QMDB applied marker names exactly the
// given block. The miner uses it to check that a parked speculative build
// is still valid: the marker moves when another view's proposal is applied
// locally, and a seal built on the stale parent is rejected later by
// checkQMDBLeaderSealParent. Returns true when there is no marker (fresh
// or non-speculative data — nothing to contradict).
func (bc *BlockChain) AppliedHeadIs(hash types.Hash) bool {
	if !bc.qmdbEnabled {
		return true
	}
	var (
		appliedHash types.Hash
		haveMarker  bool
	)
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		_, h, ok, err := rawdb.ReadQMDBApplied(tx)
		if err == nil && ok {
			appliedHash, haveMarker = types.Hash(h), true
		}
		return nil
	})
	if !haveMarker {
		return true
	}
	return appliedHash == hash
}

// clearReadThroughCache invalidates the layered read-through cache after a
// direct PlainState mutation that bypassed the normal import path.
func (bc *BlockChain) clearReadThroughCache() {
	if cache := layered.ExtractCache(bc.ChainDB); cache != nil {
		cache.Clear()
	}
}

// appliedHeadNumber reports the QMDB applied marker's block number.
func (bc *BlockChain) appliedHeadNumber() (uint64, bool) {
	var num uint64
	var have bool
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		an, _, ok, err := rawdb.ReadQMDBApplied(tx)
		if err == nil && ok {
			num, have = an, true
		}
		return nil
	})
	return num, have
}

func (bc *BlockChain) AppliedHeadIsExactly(hash types.Hash, number uint64) bool {
	at := false
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		an, ah, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil || !ok {
			return nil
		}
		at = an == number && types.Hash(ah) == hash
		return nil
	})
	return at
}

// HasAppliedBlock reports whether the block's state has actually been
// EXECUTED onto the world state — it sits on the applied-marker lineage —
// as opposed to merely having its body stored, future-queued, or named by a
// proposal. This is the evidence level consensus voting needs: an import-
// gated vote certified by body presence let a quorum commit a block whose
// state no honest node could ever reach (observed live: a forked leader's
// block was "already imported" on six nodes that had only stored it, got its
// QC, and wedged the whole network's committed head on an inexecutable
// block). Canonical membership is deliberately insufficient: commit can
// rewrite canonical rows before a lagging QMDB state has switched off a losing
// speculative branch. QMDB chains require an applied marker; only non-QMDB
// chains and E=1 light followers retain the stored-header fallback. A light
// follower (N42_E1_SOURCE, bench only) never executes and never writes a
// marker: it stores each block with the executor's result, so a stored header
// is its only, and sufficient, evidence. Without the fallback every commit on
// a follower reads as unexecuted and fires a catch-up range fetch.
func (bc *BlockChain) HasAppliedBlock(hash types.Hash, number uint64) bool {
	applied := false
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		an, ah, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return nil
		}
		if !ok {
			applied = (!bc.qmdbEnabled || bc.E1Light()) && rawdb.ReadHeader(tx, hash, number) != nil
			return nil
		}
		if number > an {
			return nil // above the applied head — cannot have been executed
		}
		// The applied marker's lineage is the only execution evidence. Do not
		// trust the canonical table here: CommitToCanonical may already name a
		// winning sibling while QMDB still sits on a losing speculative branch.
		// Walk only the recent undo window; consensus proposals and catch-up
		// branch switches are necessarily within it.
		cur, curN := ah, an
		for curN > number && an-curN < 256 {
			hdr := rawdb.ReadHeader(tx, cur, curN)
			if hdr == nil || hdr.Number == nil || hdr.Number.BitLen() > 64 || hdr.Number.Uint64() != curN || hdr.Hash() != cur {
				return nil
			}
			cur = hdr.ParentHash
			curN--
		}
		applied = curN == number && cur == hash
		return nil
	})
	return applied
}
