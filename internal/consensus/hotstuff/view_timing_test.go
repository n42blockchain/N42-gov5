// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// tp returns a pointer to base+offset, for building synthetic ViewTimings.
func tp(base time.Time, offset time.Duration) *time.Time {
	t := base.Add(offset)
	return &t
}

func assertPhase(t *testing.T, name string, got PhaseDuration, wantMs int64) {
	t.Helper()
	if !got.OK {
		t.Fatalf("%s: expected a measured duration, got none", name)
	}
	if got.Ms() != wantMs {
		t.Fatalf("%s: expected %dms, got %dms", name, wantMs, got.Ms())
	}
}

func assertUnmeasured(t *testing.T, name string, got PhaseDuration) {
	t.Helper()
	if got.OK {
		t.Fatalf("%s: expected unmeasured, got %dms", name, got.Ms())
	}
	if got.Ms() != -1 {
		t.Fatalf("%s: unmeasured Ms() should be -1, got %d", name, got.Ms())
	}
}

// TestViewTimingPhasesLeader checks the leader-side stage arithmetic:
// propose = start→proposalSent, r1 = proposalSent→prepareQC,
// r2 = prepareQC→commitQC, total = start→commitQC.
func TestViewTimingPhasesLeader(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{
		View:             42,
		ViewStart:        base,
		ProposalSent:     tp(base, 800*time.Millisecond),
		PrepareQCFormed:  tp(base, 2300*time.Millisecond),
		CommitQCFormed:   tp(base, 3900*time.Millisecond),
		PrepareVoteCount: 5,
		CommitVoteCount:  6,
	}

	p := vt.Phases()
	if p.Role != RoleLeader {
		t.Fatalf("expected leader role, got %s", p.Role)
	}
	if p.View != 42 {
		t.Fatalf("expected view 42, got %d", p.View)
	}
	assertPhase(t, "propose", p.Propose, 800)
	assertPhase(t, "r1", p.Round1, 1500)
	assertPhase(t, "r2", p.Round2, 1600)
	assertPhase(t, "total", p.Total, 3900)

	// Follower-only stages must stay unmeasured on the leader path.
	assertUnmeasured(t, "recv", p.Delivery)
	assertUnmeasured(t, "exec", p.ExecWait)

	if p.PrepareVoteCount != 5 || p.CommitVoteCount != 6 {
		t.Fatalf("vote counts not carried through: %d/%d", p.PrepareVoteCount, p.CommitVoteCount)
	}

	// Stages must sum back to the total.
	if p.Propose.D+p.Round1.D+p.Round2.D != p.Total.D {
		t.Fatalf("stages do not sum to total: %v+%v+%v != %v",
			p.Propose.D, p.Round1.D, p.Round2.D, p.Total.D)
	}
}

// TestViewTimingPhasesFollower checks the follower-side stage arithmetic:
// recv = start→proposalReceived, exec = proposalReceived→voteSent,
// r1 = voteSent→commitVoteSent, r2 = commitVoteSent→commitQC.
func TestViewTimingPhasesFollower(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{
		View:             7,
		ViewStart:        base,
		ProposalReceived: tp(base, 120*time.Millisecond),
		VoteSent:         tp(base, 880*time.Millisecond),
		CommitVoteSent:   tp(base, 2380*time.Millisecond),
		CommitQCFormed:   tp(base, 3930*time.Millisecond),
	}

	p := vt.Phases()
	if p.Role != RoleFollower {
		t.Fatalf("expected follower role, got %s", p.Role)
	}
	assertPhase(t, "recv", p.Delivery, 120)
	assertPhase(t, "exec", p.ExecWait, 760)
	assertPhase(t, "r1", p.Round1, 1500)
	assertPhase(t, "r2", p.Round2, 1550)
	assertPhase(t, "total", p.Total, 3930)

	// Leader-only stage must stay unmeasured on the follower path.
	assertUnmeasured(t, "propose", p.Propose)

	if p.Delivery.D+p.ExecWait.D+p.Round1.D+p.Round2.D != p.Total.D {
		t.Fatal("follower stages do not sum to total")
	}
}

// TestViewTimingPhasesPartial covers views that never completed, timestamps
// that a role never fills, and non-monotonic clocks — none may report a bogus
// duration.
func TestViewTimingPhasesPartial(t *testing.T) {
	base := time.Now()

	// Leader that proposed but never reached a CommitQC.
	incomplete := ViewTiming{
		ViewStart:    base,
		ProposalSent: tp(base, 500*time.Millisecond),
	}
	p := incomplete.Phases()
	if p.Role != RoleLeader {
		t.Fatalf("expected leader role, got %s", p.Role)
	}
	assertPhase(t, "propose", p.Propose, 500)
	assertUnmeasured(t, "r1", p.Round1)
	assertUnmeasured(t, "r2", p.Round2)
	assertUnmeasured(t, "total", p.Total)

	// Commit adopted from a Decide without ever seeing the proposal.
	noProposal := ViewTiming{
		ViewStart:      base,
		CommitQCFormed: tp(base, 2*time.Second),
	}
	p = noProposal.Phases()
	if p.Role != RoleUnknown {
		t.Fatalf("expected unknown role, got %s", p.Role)
	}
	assertPhase(t, "total", p.Total, 2000)
	assertUnmeasured(t, "r1", p.Round1)
	assertUnmeasured(t, "r2", p.Round2)

	// Follower whose vote timestamp predates the proposal (clock stepped back).
	backwards := ViewTiming{
		ViewStart:        base,
		ProposalReceived: tp(base, 900*time.Millisecond),
		VoteSent:         tp(base, 100*time.Millisecond),
	}
	assertUnmeasured(t, "exec", backwards.Phases().ExecWait)

	// A zero ViewStart must not turn into a decades-long total.
	zeroStart := ViewTiming{CommitQCFormed: tp(base, time.Second)}
	assertUnmeasured(t, "total", zeroStart.Phases().Total)
}

// TestViewPhasesLogLine pins the compact one-line format that is grepped out
// of live-chain logs.
func TestViewPhasesLogLine(t *testing.T) {
	base := time.Now()
	leader := ViewTiming{
		View:             421,
		ViewStart:        base,
		ProposalSent:     tp(base, 812*time.Millisecond),
		PrepareQCFormed:  tp(base, 2315*time.Millisecond),
		CommitQCFormed:   tp(base, 3925*time.Millisecond),
		PrepareVoteCount: 5,
		CommitVoteCount:  5,
	}
	want := "hotstuff view timing: view=421 role=leader propose=812ms r1=1503ms r2=1610ms total=3925ms votes=5/5"
	if got := leader.Phases().LogLine(); got != want {
		t.Fatalf("leader log line mismatch:\n got %q\nwant %q", got, want)
	}

	follower := ViewTiming{
		View:             422,
		ViewStart:        base,
		ProposalReceived: tp(base, 118*time.Millisecond),
		VoteSent:         tp(base, 882*time.Millisecond),
		CommitVoteSent:   tp(base, 2384*time.Millisecond),
		CommitQCFormed:   tp(base, 3932*time.Millisecond),
	}
	want = "hotstuff view timing: view=422 role=follower recv=118ms exec=764ms r1=1502ms r2=1548ms total=3932ms"
	if got := follower.Phases().LogLine(); got != want {
		t.Fatalf("follower log line mismatch:\n got %q\nwant %q", got, want)
	}

	// Unmeasured stages are omitted, never printed as 0ms.
	partial := ViewTiming{View: 9, ViewStart: base, CommitQCFormed: tp(base, time.Second)}
	got := partial.Phases().LogLine()
	if strings.Contains(got, "propose=") || strings.Contains(got, "r1=") || strings.Contains(got, "votes=") {
		t.Fatalf("partial line leaked unmeasured stages: %q", got)
	}
	if !strings.Contains(got, "total=1000ms") {
		t.Fatalf("partial line missing total: %q", got)
	}
}

// TestLastCommittedTimingCopy verifies the getter hands back an isolated copy:
// a caller mutating the result must not corrupt engine state, and a later
// commit must not retroactively change an already-returned value.
func TestLastCommittedTimingCopy(t *testing.T) {
	e := &ConsensusEngine{}

	if _, ok := e.LastCommittedTiming(); ok {
		t.Fatal("expected no timing before the first commit")
	}
	if _, ok := e.LastCommittedPhases(); ok {
		t.Fatal("expected no phases before the first commit")
	}

	base := time.Now()
	first := ViewTiming{View: 1, ViewStart: base, ProposalSent: tp(base, time.Second), CommitQCFormed: tp(base, 2*time.Second)}
	e.publishCommittedTiming(first)

	got, ok := e.LastCommittedTiming()
	if !ok {
		t.Fatal("expected timing after commit")
	}
	got.View = 999 // caller mutation must not leak into the engine

	second := ViewTiming{View: 2, ViewStart: base, CommitQCFormed: tp(base, 3*time.Second)}
	e.publishCommittedTiming(second)

	if got.View != 999 {
		t.Fatal("previously returned copy was mutated by a later commit")
	}
	latest, _ := e.LastCommittedTiming()
	if latest.View != 2 {
		t.Fatalf("expected latest view 2, got %d", latest.View)
	}
	phases, ok := e.LastCommittedPhases()
	if !ok || phases.View != 2 || phases.Total.Ms() != 3000 {
		t.Fatalf("unexpected phases: %+v", phases)
	}
}

// TestLastCommittedTimingConcurrent hammers the getter against the writer.
// Run with -race: the getter must be safe for out-of-engine readers while
// consensus keeps committing views.
func TestLastCommittedTimingConcurrent(t *testing.T) {
	e := &ConsensusEngine{}
	const rounds = 200

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: mimics advanceToView publishing each committed view.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		base := time.Now()
		for i := 1; i <= rounds; i++ {
			e.publishCommittedTiming(ViewTiming{
				View:             ViewNumber(i),
				ViewStart:        base,
				ProposalSent:     tp(base, time.Duration(i)*time.Millisecond),
				PrepareQCFormed:  tp(base, time.Duration(i+1)*time.Millisecond),
				CommitQCFormed:   tp(base, time.Duration(i+2)*time.Millisecond),
				PrepareVoteCount: uint32(i),
			})
		}
	}()

	// Readers.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if timing, ok := e.LastCommittedTiming(); ok {
					// Reading through the copy must never observe a torn value.
					if timing.CommitQCFormed != nil && timing.ProposalSent != nil &&
						timing.CommitQCFormed.Before(*timing.ProposalSent) {
						t.Error("observed torn ViewTiming")
						return
					}
					_ = timing.Phases()
				}
			}
		}()
	}

	wg.Wait()

	final, ok := e.LastCommittedTiming()
	if !ok || final.View != rounds {
		t.Fatalf("expected final view %d, got %d (ok=%v)", rounds, final.View, ok)
	}
}

// TestEngineCommitPublishesTiming drives a real single-validator commit and
// checks the timing is published through advanceToView, with the leader stages
// populated and the view reset for the next round.
func TestEngineCommitPublishesTiming(t *testing.T) {
	setup := newTestSetup(t, 1)
	engine, outputCh := newTestEngine(t, setup, 0)

	if _, ok := engine.LastCommittedTiming(); ok {
		t.Fatal("expected no timing before the first commit")
	}

	blockHash := types.Hash{0xab}
	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatalf("block ready: %v", err)
	}
	drainOutputs(outputCh)

	timing, ok := engine.LastCommittedTiming()
	if !ok {
		t.Fatal("expected the committed view's timing to be published")
	}
	if timing.View != 1 {
		t.Fatalf("expected committed view 1, got %d", timing.View)
	}

	p := timing.Phases()
	if p.Role != RoleLeader {
		t.Fatalf("expected leader role, got %s", p.Role)
	}
	for name, d := range map[string]PhaseDuration{
		"propose": p.Propose, "r1": p.Round1, "r2": p.Round2, "total": p.Total,
	} {
		if !d.OK {
			t.Fatalf("%s was not measured: %s", name, p.LogLine())
		}
		if d.D < 0 || d.D > time.Minute {
			t.Fatalf("%s out of range: %v", name, d.D)
		}
	}
	if p.PrepareVoteCount != 1 || p.CommitVoteCount != 1 {
		t.Fatalf("expected 1/1 votes for a single validator, got %d/%d",
			p.PrepareVoteCount, p.CommitVoteCount)
	}

	// The engine must have started a fresh timing for the next view.
	engine.mu.Lock()
	current := engine.viewTiming
	engine.mu.Unlock()
	if current.View != 2 {
		t.Fatalf("expected the new view timing to track view 2, got %d", current.View)
	}
	if current.CommitQCFormed != nil || current.ProposalSent != nil {
		t.Fatal("new view timing was not reset")
	}
}
