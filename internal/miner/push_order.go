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
	"strconv"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
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

// commitVoteJournalWaiter is implemented by leader-driven consensus engines
// (HotStuff) so the miner's own write path can delay the START of
// WriteBlockWithState for a block it just sealed until this node's own
// commit-vote journal write for that exact block has succeeded -- or a
// configured timeout elapses, or the view is abandoned, whichever is first
// (S19, docs/QS_BLOCK_TIME_BUDGET.md 6cn/6co: journalCommitVote today races
// this same write for the single MDBX writer, adding ~330 ms to Round2 on
// full blocks; letting the journal go first while the writer is idle, then
// starting the write, should let the two overlap the OTHER way instead).
//
// Gated behind N42_LEADER_WRITE_AFTER_JOURNAL (leaderWriteAfterJournalOn,
// below); off by default, so a round has to opt in exactly like
// N42_PUSH_BEFORE_WRITE/N42_PROPOSE_BEFORE_WRITE above.
type commitVoteJournalWaiter interface {
	WaitForCommitVoteJournal(hash types.Hash, timeout time.Duration) (time.Duration, string)
}

var (
	leaderWriteAfterJournalOnce sync.Once
	leaderWriteAfterJournalOn   bool

	leaderWriteAfterJournalTimeoutOnce sync.Once
	leaderWriteAfterJournalTimeoutMs   time.Duration
)

// LeaderWriteAfterJournalOn reports N42_LEADER_WRITE_AFTER_JOURNAL=1.
func LeaderWriteAfterJournalOn() bool {
	leaderWriteAfterJournalOnce.Do(func() {
		leaderWriteAfterJournalOn = os.Getenv("N42_LEADER_WRITE_AFTER_JOURNAL") == "1"
	})
	return leaderWriteAfterJournalOn
}

// LeaderWriteAfterJournalTimeout returns N42_LEADER_WRITE_AFTER_JOURNAL_TIMEOUT_MS
// (default 150ms) -- the ceiling on how long the write path waits for the
// journal signal before giving up and writing anyway. Parsed once, here, on
// the miner side: the miner OWNS the wait (and its timeout), the engine only
// fires the signal, per the task's own division of responsibility.
func LeaderWriteAfterJournalTimeout() time.Duration {
	leaderWriteAfterJournalTimeoutOnce.Do(func() {
		leaderWriteAfterJournalTimeoutMs = parseLeaderWriteAfterJournalTimeoutMs(os.Getenv("N42_LEADER_WRITE_AFTER_JOURNAL_TIMEOUT_MS"))
	})
	return leaderWriteAfterJournalTimeoutMs
}

// parseLeaderWriteAfterJournalTimeoutMs is LeaderWriteAfterJournalTimeout's
// parse logic, pulled out as a pure function (parsePushBeforeWrite's own
// pattern above) so it is directly testable without the sync.Once/env-var
// dependency.
func parseLeaderWriteAfterJournalTimeoutMs(v string) time.Duration {
	const defaultMs = 150
	ms := defaultMs
	if v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}
