// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S19 unit tests for write_latch.go (docs/QS_BLOCK_TIME_BUDGET.md 6co):
// latch fires-before-wait, fires-after-wait, timeout, and (where the switch
// itself is exercised) the off/no-op contract.

package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// --- Pure writeLatch primitive: no package-switch dependency, always run. ---

func TestWriteLatchFireBeforeWait(t *testing.T) {
	l := newWriteLatch()
	l.Fire("journal")
	waited, why := l.Wait(500 * time.Millisecond)
	if why != "journal" {
		t.Fatalf("why = %q, want journal", why)
	}
	if waited > 100*time.Millisecond {
		t.Fatalf("waited = %v, want near-zero (latch was already fired)", waited)
	}
}

func TestWriteLatchFireAfterWait(t *testing.T) {
	l := newWriteLatch()
	go func() {
		time.Sleep(30 * time.Millisecond)
		l.Fire("journal")
	}()
	waited, why := l.Wait(2 * time.Second)
	if why != "journal" {
		t.Fatalf("why = %q, want journal", why)
	}
	if waited < 30*time.Millisecond {
		t.Fatalf("waited = %v, want >= 30ms (the fire's own delay)", waited)
	}
}

func TestWriteLatchTimeout(t *testing.T) {
	l := newWriteLatch()
	waited, why := l.Wait(20 * time.Millisecond)
	if why != "timeout" {
		t.Fatalf("why = %q, want timeout", why)
	}
	if waited < 20*time.Millisecond {
		t.Fatalf("waited = %v, want >= 20ms", waited)
	}
}

func TestWriteLatchDoubleFireFirstWins(t *testing.T) {
	l := newWriteLatch()
	l.Fire("journal")
	l.Fire("abandoned") // must be a silent no-op, not a panic (close of closed channel)
	_, why := l.Wait(time.Second)
	if why != "journal" {
		t.Fatalf("why = %q, want journal (first Fire wins)", why)
	}
}

// --- Engine-level get-or-create / fire / wait: needs leaderWriteAfterJournalEnabled
// (a package-level var read once at process start, so it cannot be toggled
// per-test) since WaitForCommitVoteJournal itself short-circuits to "off"
// otherwise. Run once with N42_LEADER_WRITE_AFTER_JOURNAL=1 exported before
// go test to exercise these; skipped otherwise, matching the S18 precedent
// for contentionDiagEnabled-gated tests. ---

func TestWaitForCommitVoteJournalOffIsNoOp(t *testing.T) {
	if leaderWriteAfterJournalEnabled {
		t.Skip("this test checks the OFF path; N42_LEADER_WRITE_AFTER_JOURNAL=1 is set in this test binary")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	waited, why := e.WaitForCommitVoteJournal(types.Hash{1, 2, 3}, 500*time.Millisecond)
	if why != "off" {
		t.Fatalf("why = %q, want off", why)
	}
	if waited != 0 {
		t.Fatalf("waited = %v, want 0 (must return immediately when the switch is off)", waited)
	}
	if e.writeLatches != nil {
		t.Fatal("writeLatches map was touched while the switch is off")
	}
}

func TestFireWriteLatchThenWaitReturnsImmediately(t *testing.T) {
	if !leaderWriteAfterJournalEnabled {
		t.Skip("N42_LEADER_WRITE_AFTER_JOURNAL=1 not set in this test binary's environment")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	hash := types.Hash{4, 5, 6}
	e.fireWriteLatch(hash, "journal")
	waited, why := e.WaitForCommitVoteJournal(hash, 500*time.Millisecond)
	if why != "journal" {
		t.Fatalf("why = %q, want journal", why)
	}
	if waited > 100*time.Millisecond {
		t.Fatalf("waited = %v, want near-zero (fire-before-wait)", waited)
	}
}

func TestWaitForCommitVoteJournalThenFireReleasesIt(t *testing.T) {
	if !leaderWriteAfterJournalEnabled {
		t.Skip("N42_LEADER_WRITE_AFTER_JOURNAL=1 not set in this test binary's environment")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	hash := types.Hash{7, 8, 9}
	go func() {
		time.Sleep(30 * time.Millisecond)
		e.fireWriteLatch(hash, "journal")
	}()
	waited, why := e.WaitForCommitVoteJournal(hash, 2*time.Second)
	if why != "journal" {
		t.Fatalf("why = %q, want journal", why)
	}
	if waited < 30*time.Millisecond {
		t.Fatalf("waited = %v, want >= 30ms", waited)
	}
}

func TestWaitForCommitVoteJournalTimesOutWithoutAFire(t *testing.T) {
	if !leaderWriteAfterJournalEnabled {
		t.Skip("N42_LEADER_WRITE_AFTER_JOURNAL=1 not set in this test binary's environment")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	waited, why := e.WaitForCommitVoteJournal(types.Hash{10}, 20*time.Millisecond)
	if why != "timeout" {
		t.Fatalf("why = %q, want timeout", why)
	}
	if waited < 20*time.Millisecond {
		t.Fatalf("waited = %v, want >= 20ms", waited)
	}
}

// TestWaitForCommitVoteJournalClaimDeletesEntry checks the consuming side
// (WaitForCommitVoteJournal) removes the map entry once it has read the
// result, so a claimed latch does not linger.
func TestWaitForCommitVoteJournalClaimDeletesEntry(t *testing.T) {
	if !leaderWriteAfterJournalEnabled {
		t.Skip("N42_LEADER_WRITE_AFTER_JOURNAL=1 not set in this test binary's environment")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	hash := types.Hash{11}
	e.fireWriteLatch(hash, "journal")
	if _, why := e.WaitForCommitVoteJournal(hash, time.Second); why != "journal" {
		t.Fatalf("why = %q, want journal", why)
	}
	e.writeLatchMu.Lock()
	_, stillThere := e.writeLatches[hash]
	e.writeLatchMu.Unlock()
	if stillThere {
		t.Fatal("writeLatches entry was not removed after being claimed")
	}
}

// TestAdvanceToViewFiresAbandonedForUnjournalledSelfProposal exercises the
// timeout case directly at the point advanceToView touches: a leader's own
// proposed hash is recorded (selfProposalHash) but journalCommitVote never
// ran for it (PrepareQC never formed), and the view advances anyway (a
// pacemaker timeout in production). advanceToView must release the write
// latch with why "abandoned" and clear selfProposalHash so a later,
// unrelated view does not see a stale pending hash.
func TestAdvanceToViewFiresAbandonedForUnjournalledSelfProposal(t *testing.T) {
	if !leaderWriteAfterJournalEnabled {
		t.Skip("N42_LEADER_WRITE_AFTER_JOURNAL=1 not set in this test binary's environment")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	hash := types.Hash{12, 13}
	e.selfProposalHash = hash

	if err := e.advanceToView(e.roundState.CurrentView() + 1); err != nil {
		t.Fatalf("advanceToView: %v", err)
	}

	if e.selfProposalHash != (types.Hash{}) {
		t.Fatalf("selfProposalHash = %x, want zero after advanceToView", e.selfProposalHash)
	}
	waited, why := e.WaitForCommitVoteJournal(hash, 200*time.Millisecond)
	if why != "abandoned" {
		t.Fatalf("why = %q, want abandoned", why)
	}
	if waited > 100*time.Millisecond {
		t.Fatalf("waited = %v, want near-zero (already fired by advanceToView)", waited)
	}
}

// TestAdvanceToViewLeavesUnsetSelfProposalAlone checks the common case (no
// self-proposal pending, e.g. a follower, or a leader between blocks) is a
// no-op: advanceToView must not create or fire a latch for the zero hash.
func TestAdvanceToViewLeavesUnsetSelfProposalAlone(t *testing.T) {
	if !leaderWriteAfterJournalEnabled {
		t.Skip("N42_LEADER_WRITE_AFTER_JOURNAL=1 not set in this test binary's environment")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	if err := e.advanceToView(e.roundState.CurrentView() + 1); err != nil {
		t.Fatalf("advanceToView: %v", err)
	}
	e.writeLatchMu.Lock()
	n := len(e.writeLatches)
	e.writeLatchMu.Unlock()
	if n != 0 {
		t.Fatalf("writeLatches has %d entries, want 0 (nothing was pending)", n)
	}
}

// TestGetOrCreateWriteLatchBoundedMap checks the defensive cap: inserting
// more than writeLatchMaxEntries distinct, never-claimed hashes does not
// grow the map without bound.
func TestGetOrCreateWriteLatchBoundedMap(t *testing.T) {
	if !leaderWriteAfterJournalEnabled {
		t.Skip("N42_LEADER_WRITE_AFTER_JOURNAL=1 not set in this test binary's environment")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	for i := 0; i < writeLatchMaxEntries+10; i++ {
		var h types.Hash
		h[0] = byte(i)
		h[1] = byte(i >> 8)
		e.getOrCreateWriteLatch(h)
	}
	e.writeLatchMu.Lock()
	n := len(e.writeLatches)
	e.writeLatchMu.Unlock()
	if n > writeLatchMaxEntries {
		t.Fatalf("writeLatches has %d entries, want <= %d", n, writeLatchMaxEntries)
	}
}
