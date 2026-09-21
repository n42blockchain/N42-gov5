// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S18 unit tests: journalPrepareVote/journalCommitVote timing derivation
// and rendering (jpvMs/jcvMs/jcvAt). docs/QS_BLOCK_TIME_BUDGET.md 6cm.

package hotstuff

import (
	"strings"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// TestViewPhasesJournalTiming checks Phases() derives JournalPrepareVote/
// JournalCommitVote/JournalCommitVoteAt from contentionStamps, and that an
// untouched view derives all-unmeasured.
func TestViewPhasesJournalTiming(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{View: 3, ViewStart: base, ProposalSent: tp(base, 0), PrepareQCFormed: tp(base, 400*time.Millisecond)}
	vt.Contention.journalPrepareVoteMs = 2 * time.Millisecond
	vt.Contention.journalPrepareVoteOK = true
	vt.Contention.journalCommitVoteMs = 331 * time.Millisecond
	vt.Contention.journalCommitVoteAtMs = 1758000000123
	vt.Contention.journalCommitVoteOK = true

	p := vt.Phases()
	assertPhase(t, "JournalPrepareVote", p.JournalPrepareVote, 2)
	assertPhase(t, "JournalCommitVote", p.JournalCommitVote, 331)
	if p.JournalCommitVoteAt != 1758000000123 {
		t.Fatalf("JournalCommitVoteAt = %d, want 1758000000123", p.JournalCommitVoteAt)
	}
}

func TestViewPhasesJournalTimingUnmeasured(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{View: 1, ViewStart: base, ProposalSent: tp(base, 0), PrepareQCFormed: tp(base, 10*time.Millisecond)}
	p := vt.Phases()
	assertUnmeasured(t, "JournalPrepareVote", p.JournalPrepareVote)
	assertUnmeasured(t, "JournalCommitVote", p.JournalCommitVote)
	if p.JournalCommitVoteAt != 0 {
		t.Fatalf("JournalCommitVoteAt = %d, want 0", p.JournalCommitVoteAt)
	}
}

// TestLogLineRendersJournalTiming checks jpvMs/jcvMs/jcvAt appear with the
// documented names when measured, and stay silent otherwise.
func TestLogLineRendersJournalTiming(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{View: 5, ViewStart: base, ProposalSent: tp(base, 0), PrepareQCFormed: tp(base, 300*time.Millisecond)}
	vt.Contention.journalPrepareVoteMs = 3 * time.Millisecond
	vt.Contention.journalPrepareVoteOK = true
	vt.Contention.journalCommitVoteMs = 326 * time.Millisecond
	vt.Contention.journalCommitVoteAtMs = 999
	vt.Contention.journalCommitVoteOK = true

	line := vt.Phases().LogLine()
	for _, want := range []string{"jpvMs=3ms", "jcvMs=326ms", "jcvAt=999"} {
		if !strings.Contains(line, want) {
			t.Fatalf("LogLine() = %q, missing %q", line, want)
		}
	}
}

func TestLogLineSilentWithoutJournalTiming(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{View: 1, ViewStart: base, ProposalSent: tp(base, 0), PrepareQCFormed: tp(base, 10*time.Millisecond)}
	line := vt.Phases().LogLine()
	for _, field := range []string{"jpvMs", "jcvMs", "jcvAt"} {
		if strings.Contains(line, field) {
			t.Fatalf("LogLine() = %q, unexpectedly contains %q", line, field)
		}
	}
}

// slowVoteJournal is a controllable VoteJournal for timing tests: it sleeps
// for a fixed duration before returning, so journalPrepareVote/
// journalCommitVote's own timing can be checked against a known lower bound
// without touching a real MDBX store.
type slowVoteJournal struct {
	delay time.Duration
}

func (j *slowVoteJournal) JournalVote(*ConsensusState) error {
	if j.delay > 0 {
		time.Sleep(j.delay)
	}
	return nil
}

// TestJournalCommitVoteTimingReflectsJournalDelay checks that
// journalCommitVote's own timing (jcvMs) reflects at least the delay a slow
// VoteJournal.JournalVote call takes -- i.e. the stamp really does wrap the
// call, not some unrelated span. Uses the same newTestSetup/newTestEngine
// harness as the rest of this package's engine tests (hotstuff_test.go).
func TestJournalCommitVoteTimingReflectsJournalDelay(t *testing.T) {
	if !contentionDiagEnabled {
		t.Skip("N42_CONTENTION_DIAG=1 not set in this test binary's environment; recording is a no-op when off")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.SetVoteJournal(&slowVoteJournal{delay: 20 * time.Millisecond})

	before := time.Now()
	if err := e.journalCommitVote(1, types.Hash{1, 2, 3}); err != nil {
		t.Fatalf("journalCommitVote: %v", err)
	}
	if e.viewTiming.Contention.journalCommitVoteMs < 20*time.Millisecond {
		t.Fatalf("journalCommitVoteMs = %v, want >= 20ms (the slow journal's own delay)", e.viewTiming.Contention.journalCommitVoteMs)
	}
	if !e.viewTiming.Contention.journalCommitVoteOK {
		t.Fatal("journalCommitVoteOK = false, want true")
	}
	if e.viewTiming.Contention.journalCommitVoteAtMs < before.UnixMilli() {
		t.Fatalf("journalCommitVoteAtMs = %d, want >= %d (the call's own start)", e.viewTiming.Contention.journalCommitVoteAtMs, before.UnixMilli())
	}
}

// TestJournalPrepareVoteTimingReflectsJournalDelay is
// TestJournalCommitVoteTimingReflectsJournalDelay for the Round 1 journal
// call.
func TestJournalPrepareVoteTimingReflectsJournalDelay(t *testing.T) {
	if !contentionDiagEnabled {
		t.Skip("N42_CONTENTION_DIAG=1 not set in this test binary's environment; recording is a no-op when off")
	}
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.SetVoteJournal(&slowVoteJournal{delay: 15 * time.Millisecond})

	if err := e.journalPrepareVote(1, types.Hash{4, 5, 6}); err != nil {
		t.Fatalf("journalPrepareVote: %v", err)
	}
	if e.viewTiming.Contention.journalPrepareVoteMs < 15*time.Millisecond {
		t.Fatalf("journalPrepareVoteMs = %v, want >= 15ms", e.viewTiming.Contention.journalPrepareVoteMs)
	}
	if !e.viewTiming.Contention.journalPrepareVoteOK {
		t.Fatal("journalPrepareVoteOK = false, want true")
	}
}
