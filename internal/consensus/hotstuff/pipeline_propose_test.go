package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func buildDecide(t *testing.T, setup *testSetup, blockHash types.Hash) *Decide {
	t.Helper()
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())
	for i := 0; i < 3; i++ {
		_ = vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(CommitSigningMessage(1, blockHash)))
	}
	qc, err := vc.BuildQCWithMessage(setup.vs, CommitSigningMessage(1, blockHash))
	if err != nil {
		t.Fatal(err)
	}
	return &Decide{View: 1, BlockHash: blockHash, CommitQC: *qc}
}

func deliverDecide(t *testing.T, e *ConsensusEngine, d *Decide) {
	t.Helper()
	if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgDecide, Payload: d}}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
}

func viewChanges(outs []EngineOutput) []ViewNumber {
	var vs []ViewNumber
	for _, o := range outs {
		if o.Type == OutputViewChanged {
			vs = append(vs, o.View)
		}
	}
	return vs
}

// The leader enters view 2 on its PrepareQC; the commit votes for view 1
// still arrive, form the CommitQC and commit the block without another view
// change. Off, the view changes only at the CommitQC.
func TestPipelineProposeLeaderEntersNextViewOnPrepareQC(t *testing.T) {
	for _, on := range []bool{false, true} {
		restore := SetPipelineProposeForTest(on)
		setup := newTestSetup(t, 4)
		leader, outputCh := newTestEngine(t, setup, 1)
		blockHash := types.Hash{0xD1}

		outs := leaderToPrepareQC(t, setup, leader, outputCh, blockHash)
		if countOutputs(outs, OutputBroadcast, MsgPrepareQC) != 1 {
			t.Fatalf("on=%v: PrepareQC not broadcast", on)
		}
		vcs := viewChanges(outs)
		if on && (len(vcs) != 1 || vcs[0] != 2 || leader.CurrentView() != 2) {
			t.Fatalf("on: view changes %v, current %d; want [2], 2", vcs, leader.CurrentView())
		}
		if !on && (len(vcs) != 0 || leader.CurrentView() != 1) {
			t.Fatalf("off: view changed on the PrepareQC: %v", vcs)
		}

		sendCommitVotes(t, setup, leader, blockHash, 0, 2)
		outs = drainOutputs(outputCh)
		if countOutputs(outs, OutputBlockCommitted, 0) != 1 || countOutputs(outs, OutputBroadcast, MsgDecide) != 1 {
			t.Fatalf("on=%v: block not committed by the CommitQC", on)
		}
		vcs = viewChanges(outs)
		if on && (len(vcs) != 0 || leader.CurrentView() != 2 || leader.CurrentPhase() != PhaseWaitingForProposal) {
			t.Fatalf("on: CommitQC moved the view or phase: %v, view %d, phase %v", vcs, leader.CurrentView(), leader.CurrentPhase())
		}
		if !on && (len(vcs) != 1 || vcs[0] != 2) {
			t.Fatalf("off: view changes at the CommitQC %v, want [2]", vcs)
		}
		if got := leader.LastCommittedQC(); got.View != 1 || got.BlockHash != blockHash {
			t.Fatalf("on=%v: last committed %d/%x", on, got.View, got.BlockHash[:2])
		}
		if leader.trailing != nil {
			t.Fatalf("on=%v: trailing round left after its commit", on)
		}
		restore()
	}
}

// A follower commit votes view 1, locks on its PrepareQC and enters view 2;
// the Decide for view 1 then commits without moving the view. As view 2's
// leader it proposes on view 1's block.
func TestPipelineProposeFollowerCommitsTrailingDecide(t *testing.T) {
	defer SetPipelineProposeForTest(true)()
	setup := newTestSetup(t, 4)
	next, outputCh := newTestEngine(t, setup, 2) // leads view 2
	blockHash := types.Hash{0xD2}

	deliverPrepareQC(t, next, buildPrepareQCMsg(t, setup, blockHash))
	outs := drainOutputs(outputCh)
	if countOutputs(outs, OutputSendToValidator, MsgCommitVote) != 1 {
		t.Fatal("no commit vote for view 1")
	}
	if vcs := viewChanges(outs); len(vcs) != 1 || vcs[0] != 2 {
		t.Fatalf("view changes %v, want [2]", vcs)
	}
	if lq := next.LockedQC(); lq.View != 1 || lq.BlockHash != blockHash {
		t.Fatalf("lock %d/%x, want view 1's PrepareQC", lq.View, lq.BlockHash[:2])
	}

	deliverDecide(t, next, buildDecide(t, setup, blockHash))
	outs = drainOutputs(outputCh)
	if countOutputs(outs, OutputBlockCommitted, 0) != 1 || len(viewChanges(outs)) != 0 {
		t.Fatalf("trailing Decide: committed %d, view changes %v", countOutputs(outs, OutputBlockCommitted, 0), viewChanges(outs))
	}
	if next.CurrentView() != 2 || next.CurrentPhase() != PhaseWaitingForProposal {
		t.Fatalf("view %d phase %v after the trailing Decide", next.CurrentView(), next.CurrentPhase())
	}

	child := types.Hash{0xD3}
	if err := next.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: child}); err != nil {
		t.Fatal(err)
	}
	outs = drainOutputs(outputCh)
	var prop *Proposal
	for _, o := range outs {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgProposal {
			prop = o.Message.Payload.(*Proposal)
		}
	}
	if prop == nil || prop.View != 2 || prop.JustifyQC.View != 1 || prop.JustifyQC.BlockHash != blockHash {
		t.Fatalf("view 2 proposal %+v", prop)
	}
}

// A two-phase follower holds its commit vote for the block's attestation but
// enters view 2 at once; the attestation releases the vote for view 1 from
// view 2 without touching view 2's phase.
func TestPipelineProposeHeldCommitVoteReleasedFromNextView(t *testing.T) {
	defer SetPipelineProposeForTest(true)()
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.SetTwoPhaseVote(true)
	blockHash := types.Hash{0xD4}
	parentHash := types.Hash{0xA4}

	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, blockHash))
	outs := drainOutputs(outputCh)
	if countOutputs(outs, OutputSendToValidator, MsgCommitVote) != 0 {
		t.Fatal("commit vote before attestation")
	}
	if follower.CurrentView() != 2 {
		t.Fatalf("view %d, want 2 while the vote is held", follower.CurrentView())
	}

	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: parentHash}); err != nil {
		t.Fatal(err)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
		Hash: parentHash}); err != nil {
		t.Fatal(err)
	}
	outs = drainOutputs(outputCh)
	var cv *CommitVote
	for _, o := range outs {
		if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgCommitVote {
			cv = o.Message.Payload.(*CommitVote)
			if o.Target != 1 {
				t.Fatalf("commit vote sent to %d, want view 1's leader", o.Target)
			}
		}
	}
	if cv == nil || cv.View != 1 || cv.BlockHash != blockHash {
		t.Fatalf("released commit vote %+v", cv)
	}
	if follower.CurrentView() != 2 || follower.CurrentPhase() != PhaseWaitingForProposal {
		t.Fatalf("view %d phase %v after the release", follower.CurrentView(), follower.CurrentPhase())
	}
}

// A follower that has not seen view 1's PrepareQC takes it from view 2's
// proposal: it commit votes view 1, enters view 2 and accepts the proposal.
func TestPipelineProposeJustifyStandsInForPrepareQC(t *testing.T) {
	for _, on := range []bool{false, true} {
		restore := SetPipelineProposeForTest(on)
		setup := newTestSetup(t, 4)
		follower, outputCh := newTestEngine(t, setup, 0)
		blockHash := types.Hash{0xD5}
		child := types.Hash{0xD6}

		pqc := buildPrepareQCMsg(t, setup, blockHash)
		prop := &Proposal{View: 2, BlockHash: child, JustifyQC: pqc.QC, Proposer: 2,
			Signature: setup.keys[2].Sign(SigningMessage(2, child)).Marshal()}
		if err := follower.ProcessEvent(ConsensusEvent{Type: EventMessage,
			Msg: ConsensusMsg{Type: MsgProposal, Payload: prop}}); err != nil {
			t.Fatalf("on=%v: proposal: %v", on, err)
		}
		outs := drainOutputs(outputCh)
		cvs := countOutputs(outs, OutputSendToValidator, MsgCommitVote)
		if on && (cvs != 1 || follower.CurrentView() != 2 || follower.CurrentPhase() != PhaseVoting) {
			t.Fatalf("on: commit votes %d, view %d, phase %v", cvs, follower.CurrentView(), follower.CurrentPhase())
		}
		if !on && (cvs != 0 || follower.CurrentView() != 1) {
			t.Fatalf("off: commit votes %d, view %d; want the proposal buffered", cvs, follower.CurrentView())
		}
		restore()
	}
}

// A timed-out view is not left early: the PrepareQC still gets its commit
// vote, but the node stays in the view (its timeout for the view is out).
func TestPipelineProposeNotAfterTimeout(t *testing.T) {
	defer SetPipelineProposeForTest(true)()
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.roundState.Timeout()

	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, types.Hash{0xD7}))
	outs := drainOutputs(outputCh)
	if len(viewChanges(outs)) != 0 || follower.CurrentView() != 1 || follower.trailing != nil {
		t.Fatalf("timed-out view left early: %v, view %d", viewChanges(outs), follower.CurrentView())
	}
}

// view 2's PrepareQC replaces a trailing view 1 that never committed; view
// 1's late Decide is then ignored, and view 2's commits through its own.
func TestPipelineProposeSupersededTrailingRound(t *testing.T) {
	defer SetPipelineProposeForTest(true)()
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	b1 := types.Hash{0xE1}
	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, b1))
	if follower.CurrentView() != 2 || follower.trailing == nil || follower.trailing.view != 1 {
		t.Fatal("no trailing view 1")
	}

	b2 := types.Hash{0xE2}
	vc := NewVoteCollector(2, b2, setup.vs.Len())
	for i := 0; i < 3; i++ {
		_ = vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(SigningMessage(2, b2)))
	}
	qc, err := vc.BuildQC(setup.vs)
	if err != nil {
		t.Fatal(err)
	}
	before := pipelineSuperseded.Load()
	deliverPrepareQC(t, follower, &PrepareQCMsg{View: 2, BlockHash: b2, QC: *qc})
	if follower.CurrentView() != 3 || follower.trailing == nil || follower.trailing.view != 2 {
		t.Fatalf("view %d trailing %+v", follower.CurrentView(), follower.trailing)
	}
	if pipelineSuperseded.Load() != before+1 {
		t.Fatal("superseded round not counted")
	}
	drainOutputs(outputCh)
	deliverDecide(t, follower, buildDecide(t, setup, b1))
	if n := countOutputs(drainOutputs(outputCh), OutputBlockCommitted, 0); n != 0 {
		t.Fatalf("late Decide for the dropped view committed %d", n)
	}
}
