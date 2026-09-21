// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S14 unit tests: the vote-path contention stamp aggregation
// (roundContention.record/phase, and its rendering into ViewPhases/LogLine).
// docs/QS_BLOCK_TIME_BUDGET.md 6cb-6ce.

package hotstuff

import (
	"strings"
	"testing"
	"time"
)

// TestRoundContentionRecordSumsAndMax checks the running sum/max of
// lock-wait and the sum of work across several votes.
func TestRoundContentionRecordSumsAndMax(t *testing.T) {
	base := time.Now()
	var r roundContention

	// Vote 1: arrives at t=0, locked at t=10ms (10ms lock wait), done at
	// t=12ms (2ms work). Not quorum-reaching.
	r.record(base, base.Add(10*time.Millisecond), base.Add(12*time.Millisecond), false)
	// Vote 2: arrives at t=1ms, locked at t=40ms (39ms lock wait -- the new
	// max), done at t=41ms (1ms work). Not quorum-reaching.
	r.record(base.Add(time.Millisecond), base.Add(40*time.Millisecond), base.Add(41*time.Millisecond), false)
	// Vote 3: arrives at t=2ms, locked at t=45ms (43ms lock wait), done at
	// t=47ms (2ms work). This one completes quorum.
	r.record(base.Add(2*time.Millisecond), base.Add(45*time.Millisecond), base.Add(47*time.Millisecond), true)

	if r.n != 3 {
		t.Fatalf("n = %d, want 3", r.n)
	}
	wantLockWaitSum := 10*time.Millisecond + 39*time.Millisecond + 43*time.Millisecond
	if r.lockWaitSum != wantLockWaitSum {
		t.Fatalf("lockWaitSum = %v, want %v", r.lockWaitSum, wantLockWaitSum)
	}
	if r.lockWaitMax != 43*time.Millisecond {
		t.Fatalf("lockWaitMax = %v, want 43ms", r.lockWaitMax)
	}
	wantWorkSum := 2*time.Millisecond + 1*time.Millisecond + 2*time.Millisecond
	if r.workSum != wantWorkSum {
		t.Fatalf("workSum = %v, want %v", r.workSum, wantWorkSum)
	}
	if !r.kthSet {
		t.Fatal("kthSet = false, want true (vote 3 completed quorum)")
	}
	if !r.kthArrival.Equal(base.Add(2 * time.Millisecond)) {
		t.Fatalf("kthArrival = %v, want vote 3's arrival (base+2ms)", r.kthArrival)
	}
}

// TestRoundContentionRecordKthIsFirstQuorumCompletingVote checks that only
// the FIRST vote to report quorumReached=true sets kthArrival -- a later
// vote (e.g. a duplicate or an out-of-order re-check) must not overwrite it.
func TestRoundContentionRecordKthIsFirstQuorumCompletingVote(t *testing.T) {
	base := time.Now()
	var r roundContention

	r.record(base, base.Add(time.Millisecond), base.Add(2*time.Millisecond), true) // first to complete quorum
	r.record(base.Add(50*time.Millisecond), base.Add(51*time.Millisecond), base.Add(52*time.Millisecond), true)

	if r.n != 2 {
		t.Fatalf("n = %d, want 2", r.n)
	}
	if !r.kthArrival.Equal(base) {
		t.Fatalf("kthArrival = %v, want the FIRST quorum-completing vote's arrival (base)", r.kthArrival)
	}
}

// TestRoundContentionRecordIgnoresZeroTimes checks that a call with a zero
// arrival or locked time (the "not measured" sentinel) is a no-op, so a
// caller need not guard every call site with its own zero-check.
func TestRoundContentionRecordIgnoresZeroTimes(t *testing.T) {
	var r roundContention
	r.record(time.Time{}, time.Now(), time.Now(), true)
	if r.n != 0 {
		t.Fatalf("zero arrival: n = %d, want 0 (recorded nothing)", r.n)
	}
	r.record(time.Now(), time.Time{}, time.Now(), true)
	if r.n != 0 {
		t.Fatalf("zero locked: n = %d, want 0 (recorded nothing)", r.n)
	}
}

// TestRoundContentionRecordClampsNegativeLockWait checks that a
// non-monotonic (locked before arrive -- clock adjustment or a bug upstream)
// pair is clamped to zero rather than corrupting the sum with a negative
// duration.
func TestRoundContentionRecordClampsNegativeLockWait(t *testing.T) {
	base := time.Now()
	var r roundContention
	r.record(base.Add(10*time.Millisecond), base, base.Add(time.Millisecond), false)
	if r.lockWaitSum != 0 {
		t.Fatalf("lockWaitSum = %v, want 0 (clamped)", r.lockWaitSum)
	}
	if r.n != 1 {
		t.Fatalf("n = %d, want 1 (still counted)", r.n)
	}
}

// TestRoundContentionPhaseDerivesOffsets checks phase()'s derived
// KthArrivalOffset (roundStart -> kthArrival) and QCMinusKth (kthArrival ->
// qcFormed), the "waiting for enough votes" vs "aggregating once there were
// enough" split.
func TestRoundContentionPhaseDerivesOffsets(t *testing.T) {
	base := time.Now()
	roundStart := base
	kth := base.Add(300 * time.Millisecond)
	qcFormed := base.Add(370 * time.Millisecond)

	var r roundContention
	r.record(kth, kth.Add(2*time.Millisecond), kth.Add(3*time.Millisecond), true)

	p := r.phase(&roundStart, &qcFormed)
	if p.N != 1 {
		t.Fatalf("N = %d, want 1", p.N)
	}
	if !p.KthArrivalOffset.OK || p.KthArrivalOffset.Ms() != 300 {
		t.Fatalf("KthArrivalOffset = %+v, want 300ms", p.KthArrivalOffset)
	}
	if !p.QCMinusKth.OK || p.QCMinusKth.Ms() != 70 {
		t.Fatalf("QCMinusKth = %+v, want 70ms", p.QCMinusKth)
	}
}

// TestRoundContentionPhaseEmptyWhenNoVotes checks that an untouched
// accumulator derives an all-unmeasured phase (N=0), matching a view where
// this node never processed any Round-1/Round-2 votes as leader (i.e. every
// view when this node is a follower).
func TestRoundContentionPhaseEmptyWhenNoVotes(t *testing.T) {
	var r roundContention
	start := time.Now()
	qc := start.Add(time.Second)
	p := r.phase(&start, &qc)
	if p.N != 0 {
		t.Fatalf("N = %d, want 0", p.N)
	}
	if p.LockWaitSum.OK || p.WorkSum.OK || p.KthArrivalOffset.OK {
		t.Fatalf("expected an all-unmeasured phase for an empty accumulator, got %+v", p)
	}
}

// TestViewPhasesFollowerContentionFields checks Phases()' follower-side
// derivation: proposal/prepareQC lock-wait and work, and prepareQCArrival ->
// CommitVoteSent (PrepareQCToCommitVote).
func TestViewPhasesFollowerContentionFields(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{
		View:             7,
		ViewStart:        base,
		ProposalReceived: tp(base, 100*time.Millisecond),
		VoteSent:         tp(base, 105*time.Millisecond),
		CommitVoteSent:   tp(base, 400*time.Millisecond),
		CommitQCFormed:   tp(base, 420*time.Millisecond),
	}
	vt.Contention.proposalLockWait = 5 * time.Millisecond
	vt.Contention.proposalLockWaitOK = true
	vt.Contention.proposalWork = 2 * time.Millisecond
	vt.Contention.proposalWorkOK = true
	vt.Contention.prepareQCLockWait = 8 * time.Millisecond
	vt.Contention.prepareQCLockWaitOK = true
	vt.Contention.prepareQCWork = 3 * time.Millisecond
	vt.Contention.prepareQCWorkOK = true
	vt.Contention.prepareQCArrival = base.Add(350 * time.Millisecond)

	p := vt.Phases()
	if p.Role != RoleFollower {
		t.Fatalf("expected follower role, got %s", p.Role)
	}
	assertPhase(t, "ProposalLockWait", p.ProposalLockWait, 5)
	assertPhase(t, "ProposalWork", p.ProposalWork, 2)
	assertPhase(t, "PrepareQCLockWait", p.PrepareQCLockWait, 8)
	assertPhase(t, "PrepareQCWork", p.PrepareQCWork, 3)
	// CommitVoteSent (base+400ms) - prepareQCArrival (base+350ms) = 50ms.
	assertPhase(t, "PrepareQCToCommitVote", p.PrepareQCToCommitVote, 50)
}

// TestViewPhasesCommitVoteHeldAndGate checks the held/immediate
// classification and gate name pass through Phases() unchanged.
func TestViewPhasesCommitVoteHeldAndGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		held bool
		gate string
	}{
		{"immediate", false, ""},
		{"held-own-import", true, "own-import"},
		{"held-parent-import", true, "parent-import"},
		{"held-checked", true, "checked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := time.Now()
			vt := ViewTiming{View: 1, ViewStart: base, ProposalReceived: tp(base, time.Millisecond)}
			vt.Contention.commitVoteHeld = tc.held
			vt.Contention.commitVoteGate = tc.gate

			p := vt.Phases()
			if p.CommitVoteHeld != tc.held {
				t.Fatalf("CommitVoteHeld = %v, want %v", p.CommitVoteHeld, tc.held)
			}
			if p.CommitVoteGate != tc.gate {
				t.Fatalf("CommitVoteGate = %q, want %q", p.CommitVoteGate, tc.gate)
			}
		})
	}
}

// TestLogLineSilentWithoutContentionData checks that LogLine's output is
// unchanged (no new fields appended) when the contention accumulators are
// untouched -- the N42_CONTENTION_DIAG=0 (default) case, byte-for-byte.
func TestLogLineSilentWithoutContentionData(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{
		View:            1,
		ViewStart:       base,
		ProposalSent:    tp(base, 10*time.Millisecond),
		PrepareQCFormed: tp(base, 20*time.Millisecond),
		CommitQCFormed:  tp(base, 30*time.Millisecond),
	}
	line := vt.Phases().LogLine()
	for _, field := range []string{"r1n=", "r2n=", "propLw=", "pqcLw=", "cvHeld=", "cvGate="} {
		if strings.Contains(line, field) {
			t.Fatalf("LogLine() = %q, unexpectedly contains %q with no contention data recorded", line, field)
		}
	}
}

// TestLogLineRendersContentionFields checks that a populated leader round
// and follower commit-vote decision both show up in LogLine's output with
// the documented short field names.
func TestLogLineRendersContentionFields(t *testing.T) {
	base := time.Now()

	t.Run("leader round1", func(t *testing.T) {
		vt := ViewTiming{View: 5, ViewStart: base, ProposalSent: tp(base, 0), PrepareQCFormed: tp(base, 100*time.Millisecond)}
		vt.Contention.round1.record(base, base.Add(2*time.Millisecond), base.Add(5*time.Millisecond), true)
		line := vt.Phases().LogLine()
		for _, want := range []string{"r1n=1", "r1lw=2ms", "r1lwMax=2ms", "r1wk=3ms", "r1kth=0ms", "r1qk=100ms"} {
			if !strings.Contains(line, want) {
				t.Fatalf("LogLine() = %q, missing %q", line, want)
			}
		}
	})

	t.Run("follower held commit vote", func(t *testing.T) {
		vt := ViewTiming{View: 6, ViewStart: base, ProposalReceived: tp(base, time.Millisecond)}
		vt.Contention.prepareQCLockWait = 4 * time.Millisecond
		vt.Contention.prepareQCLockWaitOK = true
		vt.Contention.prepareQCWork = 6 * time.Millisecond
		vt.Contention.prepareQCWorkOK = true
		vt.Contention.commitVoteHeld = true
		vt.Contention.commitVoteGate = "parent-import"
		line := vt.Phases().LogLine()
		for _, want := range []string{"pqcLw=4ms", "pqcWk=6ms", "cvHeld=true", "cvGate=parent-import"} {
			if !strings.Contains(line, want) {
				t.Fatalf("LogLine() = %q, missing %q", line, want)
			}
		}
	})
}
