// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// lateCommitLeader returns a 4-validator engine that led view 1 and formed
// its CommitQC for hashA from the quorum voters, so it is now in view 2.
// outside is the one validator whose commit vote was not part of it.
func lateCommitLeader(t *testing.T) (setup *testSetup, e *ConsensusEngine, out chan EngineOutput, outside ValidatorIndex) {
	t.Helper()
	setup = newTestSetup(t, 4)
	const view = ViewNumber(1)
	leader := LeaderForView(view, setup.vs)
	e, out = newTestEngine(t, setup, int(leader))
	e.commitCollector = NewVoteCollector(view, lateHashA, setup.vs.Len())
	q := setup.vs.QuorumSize()
	// The quorum's verified R1 votes, as flushPrepareVotes records them.
	for i := 0; i < q; i++ {
		e.equivocationTracker[ValidatorIndex(i)] = lateHashA
		e.equivocationSigs[ValidatorIndex(i)] = setup.keys[i].Sign(e.voteSigningMessage(view, lateHashA)).Marshal()
	}
	voted := 0
	for i := 0; i < len(setup.keys) && voted < q; i++ {
		if err := e.ProcessEvent(lateCommitEvent(e, setup, view, ValidatorIndex(i), lateHashA)); err != nil {
			t.Fatalf("commit vote %d: %v", i, err)
		}
		voted++
		outside = ValidatorIndex(i + 1)
	}
	if got := e.CurrentView(); got != 2 {
		t.Fatalf("leader in view %d after CommitQC, want 2", got)
	}
	drainOutputs(out)
	return setup, e, out, outside
}

var (
	lateHashA = types.Hash{0xa1}
	lateHashB = types.Hash{0xb2}
)

func lateCommitEvent(e *ConsensusEngine, setup *testSetup, view ViewNumber, voter ValidatorIndex, hash types.Hash) ConsensusEvent {
	sig := setup.keys[voter].Sign(e.commitSigningMessage(view, hash))
	return ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgCommitVote,
		Payload: &CommitVote{View: view, Voter: voter, BlockHash: hash, Signature: sig.Marshal()}}}
}

func commitEvidence(t *testing.T, out chan EngineOutput) []EngineOutput {
	t.Helper()
	var ev []EngineOutput
	for _, o := range drainOutputs(out) {
		if o.Type == OutputEquivocationDetected {
			if o.EvidenceKind != EvidenceKindCommitVote {
				t.Fatalf("evidence kind %v, want commit vote", o.EvidenceKind)
			}
			ev = append(ev, o)
		}
	}
	return ev
}

func assertVotePair(t *testing.T, setup *testSetup, o EngineOutput, voter ValidatorIndex) {
	t.Helper()
	if o.Validator != voter || o.View != 1 || o.Hash1 == o.Hash2 {
		t.Fatalf("evidence %+v, want voter %d view 1 two hashes", o, voter)
	}
	pk := setup.pubKeys[voter]
	if !VerifyBLSSignature(o.Sig1, pk, o.Msg1) || !VerifyBLSSignature(o.Sig2, pk, o.Msg2) {
		t.Fatal("evidence pair does not verify")
	}
}

// A quorum member double-signs: its conflicting commit vote arrives after the
// leader's CommitQC moved it to the next view, and is still proven.
func TestLateCommitVoteConflictsWithQuorumVote(t *testing.T) {
	setup, e, out, _ := lateCommitLeader(t)
	_ = e.ProcessEvent(lateCommitEvent(e, setup, 1, 0, lateHashA))
	if ev := commitEvidence(t, out); len(ev) != 0 {
		t.Fatalf("honest repeat produced evidence: %v", ev)
	}
	_ = e.ProcessEvent(lateCommitEvent(e, setup, 1, 0, lateHashB))
	ev := commitEvidence(t, out)
	if len(ev) != 1 {
		t.Fatalf("got %d evidence outputs, want 1", len(ev))
	}
	assertVotePair(t, setup, ev[0], 0)
	// Further copies are not re-reported.
	_ = e.ProcessEvent(lateCommitEvent(e, setup, 1, 0, lateHashB))
	if ev := commitEvidence(t, out); len(ev) != 0 {
		t.Fatal("equivocation re-reported")
	}
}

// A member outside the quorum double-signs; both votes arrive late, in either
// order. The first is kept unverified, the conflict produces evidence.
func TestLateCommitVoteOutsideQuorumBothOrders(t *testing.T) {
	for _, order := range [][2]types.Hash{{lateHashA, lateHashB}, {lateHashB, lateHashA}} {
		setup, e, out, outside := lateCommitLeader(t)
		_ = e.ProcessEvent(lateCommitEvent(e, setup, 1, outside, order[0]))
		if ev := commitEvidence(t, out); len(ev) != 0 {
			t.Fatal("single late vote produced evidence")
		}
		_ = e.ProcessEvent(lateCommitEvent(e, setup, 1, outside, order[1]))
		ev := commitEvidence(t, out)
		if len(ev) != 1 {
			t.Fatalf("order %x: got %d evidence outputs, want 1", order[0][:1], len(ev))
		}
		assertVotePair(t, setup, ev[0], outside)
	}
}

// A forged first late vote (bad signature) neither produces evidence nor
// blocks the real pair that follows.
func TestLateCommitVoteForgedFirstIsReplaced(t *testing.T) {
	setup, e, out, outside := lateCommitLeader(t)
	forged := setup.keys[0].Sign(e.commitSigningMessage(1, lateHashA)) // wrong key
	_ = e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgCommitVote,
		Payload: &CommitVote{View: 1, Voter: outside, BlockHash: lateHashA, Signature: forged.Marshal()}}})
	_ = e.ProcessEvent(lateCommitEvent(e, setup, 1, outside, lateHashB))
	if ev := commitEvidence(t, out); len(ev) != 0 {
		t.Fatal("forged vote produced evidence")
	}
	_ = e.ProcessEvent(lateCommitEvent(e, setup, 1, outside, lateHashA))
	ev := commitEvidence(t, out)
	if len(ev) != 1 {
		t.Fatalf("got %d evidence outputs, want 1", len(ev))
	}
	assertVotePair(t, setup, ev[0], outside)
}

// A badly signed conflicting late vote is not evidence.
func TestLateCommitVoteBadSecondSignature(t *testing.T) {
	setup, e, out, _ := lateCommitLeader(t)
	bad := setup.keys[1].Sign(e.commitSigningMessage(1, lateHashB))
	_ = e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgCommitVote,
		Payload: &CommitVote{View: 1, Voter: 0, BlockHash: lateHashB, Signature: bad.Marshal()}}})
	if ev := commitEvidence(t, out); len(ev) != 0 {
		t.Fatal("unauthenticated conflicting vote produced evidence")
	}
}

// Beyond the window, or for a view this node did not lead, late votes are
// dropped as before.
func TestLateCommitVoteWindowAndLeader(t *testing.T) {
	setup, e, out, _ := lateCommitLeader(t)
	if err := e.advanceToView(2 + lateVoteWindow); err != nil {
		t.Fatal(err)
	}
	_ = e.ProcessEvent(lateCommitEvent(e, setup, 1, 0, lateHashB))
	if ev := commitEvidence(t, out); len(ev) != 0 {
		t.Fatal("vote beyond the window produced evidence")
	}
	if len(e.closedLedViews) > lateVoteWindow+1 {
		t.Fatalf("%d closed views kept", len(e.closedLedViews))
	}

	// A follower of view 1 keeps nothing for it.
	leader := LeaderForView(1, setup.vs)
	f, fout := newTestEngine(t, setup, int((leader+1)%4))
	if err := f.advanceToView(2); err != nil {
		t.Fatal(err)
	}
	if len(f.closedLedViews) != 0 {
		t.Fatal("follower kept a closed view")
	}
	_ = f.ProcessEvent(lateCommitEvent(f, setup, 1, 0, lateHashA))
	_ = f.ProcessEvent(lateCommitEvent(f, setup, 1, 0, lateHashB))
	if ev := commitEvidence(t, fout); len(ev) != 0 {
		t.Fatal("follower produced commit-vote evidence")
	}
}

func lateR1Event(e *ConsensusEngine, setup *testSetup, view ViewNumber, voter ValidatorIndex, hash types.Hash) ConsensusEvent {
	sig := setup.keys[voter].Sign(e.voteSigningMessage(view, hash))
	return ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgVote,
		Payload: &Vote{View: view, Voter: voter, BlockHash: hash, Signature: sig.Marshal()}}}
}

func r1Evidence(t *testing.T, out chan EngineOutput) []EngineOutput {
	t.Helper()
	var ev []EngineOutput
	for _, o := range drainOutputs(out) {
		if o.Type == OutputEquivocationDetected {
			if o.EvidenceKind != EvidenceKindVote {
				t.Fatalf("evidence kind %v, want R1 vote", o.EvidenceKind)
			}
			ev = append(ev, o)
		}
	}
	return ev
}

// AM34 item 6: a late R1 vote conflicting with the voter's verified R1 vote of
// a view this node led is proven once; an honest repeat is not evidence.
func TestLateR1VoteConflictsWithRecordedVote(t *testing.T) {
	setup, e, out, _ := lateCommitLeader(t)
	_ = e.ProcessEvent(lateR1Event(e, setup, 1, 0, lateHashA))
	if ev := r1Evidence(t, out); len(ev) != 0 {
		t.Fatal("honest late R1 vote produced evidence")
	}
	_ = e.ProcessEvent(lateR1Event(e, setup, 1, 0, lateHashB))
	ev := r1Evidence(t, out)
	if len(ev) != 1 {
		t.Fatalf("got %d evidence outputs, want 1", len(ev))
	}
	assertVotePair(t, setup, ev[0], 0)
	_ = e.ProcessEvent(lateR1Event(e, setup, 1, 0, lateHashB))
	if ev := r1Evidence(t, out); len(ev) != 0 {
		t.Fatal("R1 equivocation re-reported")
	}
}

// Both R1 votes of a member outside the quorum arrive late, in either order;
// a late commit vote does not pair with a late R1 vote of the same hash set.
func TestLateR1VoteOutsideQuorumBothOrders(t *testing.T) {
	for _, order := range [][2]types.Hash{{lateHashA, lateHashB}, {lateHashB, lateHashA}} {
		setup, e, out, outside := lateCommitLeader(t)
		_ = e.ProcessEvent(lateR1Event(e, setup, 1, outside, order[0]))
		_ = e.ProcessEvent(lateCommitEvent(e, setup, 1, outside, order[1]))
		if ev := drainOutputs(out); len(ev) != 0 {
			for _, o := range ev {
				if o.Type == OutputEquivocationDetected {
					t.Fatal("R1 and commit votes paired across rounds")
				}
			}
		}
		_ = e.ProcessEvent(lateR1Event(e, setup, 1, outside, order[1]))
		ev := r1Evidence(t, out)
		if len(ev) != 1 {
			t.Fatalf("got %d evidence outputs, want 1", len(ev))
		}
		assertVotePair(t, setup, ev[0], outside)
	}
}
