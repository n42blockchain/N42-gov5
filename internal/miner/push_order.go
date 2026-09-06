// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// N42_PUSH_BEFORE_WRITE: direct-push a sealed block before committing it,
// so a follower's import overlaps the leader's write instead of queueing
// behind it. Off unless the variable is set — this reorders the block
// production critical path and has to be earned by a round, not defaulted in.

package miner

import (
	"os"
	"sync"

	"github.com/n42blockchain/N42/common/block"
)

var (
	pushBeforeWriteOnce sync.Once
	pushBeforeWriteOn   bool
)

// PushBeforeWrite reports whether the leader should direct-push a sealed block
// before WriteBlockWithState rather than after it.
//
// The measured case for it (docs/QS_BLOCK_TIME_BUDGET.md): a leader's write is
// 206.8 ms on a full 22,857-tx block and runs BEFORE the push, so all six
// followers begin their 449 ms import 206.8 ms later than they could. The
// consensus round cannot finish until those imports do — the two-phase gate
// holds every commit vote until the block is imported locally — so that 206.8
// ms is on the critical path of a 1,166 ms block.
//
// What does NOT move: the Proposal. NotifyBlockSealed still runs after a
// successful write, so a block the write path rejects is never proposed and
// never collects a QC, exactly as today. The only new exposure is that
// followers may have imported a block the leader then abandons, which the
// existing future-queue and sibling-suppression paths already handle.
func PushBeforeWrite() bool {
	pushBeforeWriteOnce.Do(func() {
		pushBeforeWriteOn = parsePushBeforeWrite(os.Getenv("N42_PUSH_BEFORE_WRITE"))
	})
	return pushBeforeWriteOn
}

func parsePushBeforeWrite(v string) bool {
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	}
	return false
}

// sealParentChecker is the optional capability a chain implementation offers to
// answer "would the write path reject this seal as stale?" without taking the
// write lock. Declared here as a narrow assertion rather than added to
// common.IBlockChain: only the QMDB leader path has the question, and the
// read-only ethel chain has no answer for it.
type sealParentChecker interface {
	CheckSealParentApplied(blk block.IBlock) error
}

// ProposeBeforeWrite reports N42_PROPOSE_BEFORE_WRITE=1: with the early push
// on, the Proposal leaves before this node's own write as well (round 34).
// Without the early push it is inert -- there is no body with the peers to
// propose.
func ProposeBeforeWrite() bool { return os.Getenv("N42_PROPOSE_BEFORE_WRITE") == "1" }
