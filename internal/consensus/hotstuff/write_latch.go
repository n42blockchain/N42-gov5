// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S19 (docs/QS_BLOCK_TIME_BUDGET.md 6co): a small, allocation-free-after-
// construction signal from the consensus engine to the leader's own write
// path (internal/miner), so N42_LEADER_WRITE_AFTER_JOURNAL=1 can delay the
// START of WriteBlockWithState for a block THIS node proposed until its own
// commit-vote journal write for that block has succeeded (or a timeout, or
// the view is abandoned, whichever is first) -- instead of racing it for the
// single MDBX writer. Journal ordering, durability, the single
// ConsensusState record and e.mu discipline are all untouched: this is a
// pure scheduling signal, carrying no consensus data, read by nobody but the
// waiting write path.
//
// Default (N42_LEADER_WRITE_AFTER_JOURNAL unset): every method here is a
// cheap no-op (the map stays nil, Fire/Wait are never reached from the
// engine side since the two call sites that would invoke them are
// themselves gated on leaderWriteAfterJournalEnabled) -- today's behaviour
// exactly.

package hotstuff

import (
	"os"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// leaderWriteAfterJournalEnabled gates every fire site in this package
// (tryFormPrepareQC, advanceToView) and the WAIT side below. The miner
// package (push_order.go) parses the SAME env var independently into its own
// LeaderWriteAfterJournalOn() -- consistent with how every other switch in
// this campaign works (N42_CONTENTION_DIAG, N42_PUSH_BEFORE_WRITE, ...): one
// var, one name, read once per package via its own os.Getenv, no shared
// parsing plumbing across the package boundary. The timeout
// (N42_LEADER_WRITE_AFTER_JOURNAL_TIMEOUT_MS) is parsed ONLY on the miner
// side (push_order.go): the miner is the side that owns the Wait call and
// its deadline, so only it needs the value.
var leaderWriteAfterJournalEnabled = os.Getenv("N42_LEADER_WRITE_AFTER_JOURNAL") == "1"

// writeLatch is a single-hash, single-fire signal with both fire-before-wait
// and fire-after-wait semantics: a plain channel close gives both for free
// (a receive on an already-closed channel returns immediately), which is
// exactly the "must never deadlock if the signal fires before the waiter
// arrives" requirement. why is set exactly once, under fired's own guard, so
// Wait can read it race-free after the channel closes.
type writeLatch struct {
	fired chan struct{}
	mu    sync.Mutex
	why   string
	done  bool
}

func newWriteLatch() *writeLatch {
	return &writeLatch{fired: make(chan struct{})}
}

// Fire releases any current or future Wait call with why. A second Fire
// (e.g. journalCommitVote succeeding AND advanceToView's own idempotent
// abandon-check, the ordinary case) is a silent no-op — first writer wins.
func (l *writeLatch) Fire(why string) {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	l.why = why
	l.mu.Unlock()
	close(l.fired)
}

// Wait blocks until Fire is called or timeout elapses, whichever is first,
// and reports how long it actually waited plus why it returned ("journal",
// "abandoned", or "timeout"). Never holds any engine or miner lock: the
// caller must look the latch up (which does take a short leaf lock, see
// getOrCreateWriteLatch) BEFORE calling Wait.
func (l *writeLatch) Wait(timeout time.Duration) (waited time.Duration, why string) {
	start := time.Now()
	select {
	case <-l.fired:
		l.mu.Lock()
		why = l.why
		l.mu.Unlock()
	case <-time.After(timeout):
		why = "timeout"
	}
	return time.Since(start), why
}

// writeLatchMaxEntries bounds writeLatches the same way maxPendingCommits
// bounds the service's own deferred-commit map (service.go) -- one entry per
// proposed block, claimed and deleted promptly by the miner's own Wait call,
// so this is a defensive cap against a leader whose write path has stopped
// calling in, not a size this campaign's shape is expected to approach.
const writeLatchMaxEntries = 64

// getOrCreateWriteLatch returns the latch for hash, creating one if this is
// the first arrival (either side may arrive first: the engine calling Fire
// right after journalCommitVote, or the miner calling Wait right after
// sealing). Takes writeLatchMu only, never e.mu -- callers may hold e.mu
// (fireWriteLatch, called from tryFormPrepareQC/advanceToView) or must not
// (the miner's WaitForCommitVoteJournal), and this function works for both
// because it never blocks.
func (e *ConsensusEngine) getOrCreateWriteLatch(hash types.Hash) *writeLatch {
	e.writeLatchMu.Lock()
	defer e.writeLatchMu.Unlock()
	if e.writeLatches == nil {
		e.writeLatches = make(map[types.Hash]*writeLatch)
	}
	if l, ok := e.writeLatches[hash]; ok {
		return l
	}
	if len(e.writeLatches) >= writeLatchMaxEntries {
		// A node this far behind on claiming its own latches is not going to
		// catch up by keeping the old ones either; reset, matching
		// maxPendingCommits' own "a node that far behind catches up through
		// [something else], not through retries" precedent.
		e.writeLatches = make(map[types.Hash]*writeLatch)
	}
	l := newWriteLatch()
	e.writeLatches[hash] = l
	return l
}

// fireWriteLatch fires (or lazily creates-then-fires, covering the ordering
// where the engine reaches this before the miner ever calls
// WaitForCommitVoteJournal — the fire-before-wait case) the latch for hash.
// Deliberately does NOT delete the map entry: the fire-before-wait case
// needs the already-fired latch to still be there when the miner's Wait
// arrives later, or it would create a fresh, unfired one and wait out the
// full timeout despite the journal having already succeeded. Only the
// consuming side (WaitForCommitVoteJournal, below) deletes, once it has
// actually read the result; a latch nobody ever waits for (a sealed block
// dropped before reaching the write path) is bounded by
// writeLatchMaxEntries's reset instead, same as maxPendingCommits.
//
// Called from tryFormPrepareQC (why: "journal") and advanceToView (why:
// "abandoned"); both call sites already check leaderWriteAfterJournalEnabled
// before calling, so this itself does not re-check -- callers own the
// no-op-when-off contract, matching how contentionDiagEnabled's own call
// sites work elsewhere in this package.
func (e *ConsensusEngine) fireWriteLatch(hash types.Hash, why string) {
	e.getOrCreateWriteLatch(hash).Fire(why)
}

// WaitForCommitVoteJournal is the leader write path's entry point (via the
// commitVoteJournalWaiter interface in internal/miner/push_order.go), called
// from the miner's OWN goroutine (resultLoop), never from the engine's. It
// must not hold e.mu while waiting: getOrCreateWriteLatch takes only the
// separate writeLatchMu leaf lock to look the latch up, then releases it
// before the (potentially up-to-timeout-long) Wait call below.
//
// A no-op (returns immediately, why "off") when N42_LEADER_WRITE_AFTER_JOURNAL
// is unset, so a follower or a switched-off leader never touches the map at
// all -- this is the one call site outside the two fire sites that DOES
// check the switch itself, since it is reachable from outside this package
// and must be safe to call unconditionally.
func (e *ConsensusEngine) WaitForCommitVoteJournal(hash types.Hash, timeout time.Duration) (time.Duration, string) {
	if !leaderWriteAfterJournalEnabled {
		return 0, "off"
	}
	l := e.getOrCreateWriteLatch(hash)
	waited, why := l.Wait(timeout)
	e.writeLatchMu.Lock()
	delete(e.writeLatches, hash)
	e.writeLatchMu.Unlock()
	return waited, why
}
