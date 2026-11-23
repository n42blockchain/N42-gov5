// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Conflicting commits fail closed (sync review S0e / C8, owner decision Q1).
//
// CommitToCanonicalWith returned early only when the canonical hash at the
// committed height already equalled the incoming one. Any other commit at or
// below the committed height -- or one descending from a different block at
// that height -- walked down rewriting canonical rows, truncated every row
// above, and moved HotStuffCommittedHead backwards: committed history was
// silently rewritten. Two commits at one height can only come from a safety
// failure upstream (round S131 saw honest nodes commit two blocks at one
// height), so the node must refuse, alert and stop, not follow.
//
// commitConflictsWithFloor checks the incoming target against the QC-backed
// committed head (the same HotStuffCommittedHead marker the committed-block
// unwind floor and the consensus vote floor read) BEFORE anything is
// written, and before the applied-lineage guard, which would otherwise turn
// a conflict into an endless retry. N42_SYNC_LEGACY_COMMIT_REWRITE=1
// restores the rewrite.

package internal

import (
	"fmt"

	prometheus "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/synclegacy"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// maxCommitFloorWalk bounds the header walk between a commit target and the
// committed head. A longer gap is left undecided (the commit proceeds to the
// applied-lineage guard as before); catch-up ranges are at most 1024 blocks.
const maxCommitFloorWalk = 4096

var metricCommitConflict = prometheus.GetOrCreateCounter("chain_commit_conflict_total", false)

// commitConflictsWithFloor decides a commit of hash at number against the
// committed head. skip is true when the commit must not rewrite anything but
// is not a conflict (a late commit of a committed block whose canonical row is
// missing). err is a *consensus.ConflictingCommitError on a conflict.
func commitConflictsWithFloor(tx kv.Getter, hash, parent types.Hash, number uint64) (skip bool, err error) {
	if synclegacy.CommitRewrite.On() {
		return false, nil
	}
	floorHash := rawdb.ReadHotStuffCommittedHead(tx)
	if floorHash == (types.Hash{}) {
		return false, nil // nothing committed by consensus yet
	}
	fn := rawdb.ReadHeaderNumber(tx, floorHash)
	if fn == nil {
		return false, nil
	}
	floorNum := *fn
	conflict := func(height uint64, committed, incoming types.Hash) error {
		e := &consensus.ConflictingCommitError{
			Target: hash, TargetNumber: number,
			Height: height, Committed: committed, Incoming: incoming,
			CommittedHead: floorHash, CommittedHeadNumber: floorNum,
		}
		metricCommitConflict.Inc()
		log.Error("CONFLICTING COMMIT REFUSED: a commit does not extend the committed chain; "+
			"canonical rows are left untouched. This is evidence of a consensus safety failure.",
			"target", hash, "targetNumber", number, "height", height,
			"committed", committed, "incoming", incoming,
			"committedHead", floorHash, "committedHeadNumber", floorNum)
		return e
	}

	switch {
	case number == floorNum:
		if hash == floorHash {
			return false, nil
		}
		return false, conflict(number, floorHash, hash)

	case number < floorNum:
		existing, rerr := rawdb.ReadCanonicalHash(tx, number)
		if rerr != nil {
			return false, rerr
		}
		if existing == hash {
			return false, nil // late duplicate: the monotonicity guard handles it
		}
		if existing != (types.Hash{}) {
			return false, conflict(number, existing, hash)
		}
		// No canonical row: compare against the committed head's ancestor.
		anc, ok := headerAncestorAt(tx, floorHash, floorNum, number)
		if !ok {
			return true, fmt.Errorf("commit-to-canonical: %d/%x is below the committed head %d and its canonical row is missing; refusing to rewrite",
				number, hash[:8], floorNum)
		}
		if anc != hash {
			return false, conflict(number, anc, hash)
		}
		log.Warn("commit-to-canonical: late commit of a committed block with a missing canonical row; not rewriting",
			"number", number, "hash", hash, "committedHead", floorNum)
		return true, nil

	default: // number > floorNum: the target must descend from the committed head
		if number-floorNum > maxCommitFloorWalk {
			return false, nil
		}
		anc, ok := headerAncestorAt(tx, parent, number-1, floorNum)
		if !ok {
			return false, nil // ancestry not local: undecided, later guards apply
		}
		if anc != floorHash {
			return false, conflict(floorNum, floorHash, anc)
		}
		return false, nil
	}
}

// headerAncestorAt walks stored headers from (hash, number) down to height
// target and returns the hash there.
func headerAncestorAt(tx kv.Getter, hash types.Hash, number, target uint64) (types.Hash, bool) {
	if target > number || number-target > maxCommitFloorWalk {
		return types.Hash{}, false
	}
	cur := hash
	for n := number; n > target; n-- {
		hdr := rawdb.ReadHeader(tx, cur, n)
		if hdr == nil {
			return types.Hash{}, false
		}
		cur = hdr.ParentHash
	}
	return cur, true
}
