package hotstuff

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// commitJournalRecorder records the Round 2 journal writes (the snapshots
// that carry a commit vote) and how many outputs were queued at each.
type commitJournalRecorder struct {
	outputCh   chan EngineOutput
	commits    []ViewNumber
	queuedAt   []int
	failCommit bool
}

func (j *commitJournalRecorder) JournalVote(st *ConsensusState) error {
	if st.LastCommitVotedView == 0 {
		return nil // a Round 1 write
	}
	if len(j.commits) > 0 && j.commits[len(j.commits)-1] == st.LastCommitVotedView {
		return nil // a later write of an already journalled commitment
	}
	if j.failCommit {
		return errors.New("journal down")
	}
	j.commits = append(j.commits, st.LastCommitVotedView)
	j.queuedAt = append(j.queuedAt, len(j.outputCh))
	return nil
}

// A held commit vote is journalled while it waits for its attestation, and the
// release sends it without another journal write.
func TestCommitJournalEarlyJournalsWhileHeld(t *testing.T) {
	defer SetCommitJournalEarlyForTest(true)()
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.SetTwoPhaseVote(true)
	j := &commitJournalRecorder{outputCh: outputCh}
	follower.SetVoteJournal(j)

	blockHash := types.Hash{0xC1}
	parentHash := types.Hash{0xA1}
	pqc := buildPrepareQCMsg(t, setup, blockHash)

	deliverPrepareQC(t, follower, pqc)
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote before attestation: %d", n)
	}
	if len(j.commits) != 1 || j.commits[0] != 1 {
		t.Fatalf("held commitment journalled %v, want [1]", j.commits)
	}
	if got := follower.roundState.LockedQC(); got == nil || got.BlockHash != blockHash {
		t.Fatalf("lock not on the held PrepareQC: %+v", got)
	}

	// A duplicate PrepareQC (the other transport) neither journals again nor
	// releases the vote.
	deliverPrepareQC(t, follower, pqc)
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote on a duplicate PrepareQC: %d", n)
	}

	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: parentHash}); err != nil {
		t.Fatal(err)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
		Hash: parentHash}); err != nil {
		t.Fatal(err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 1 {
		t.Fatalf("commit vote after attestation: %d, want 1", n)
	}
	if len(j.commits) != 1 {
		t.Fatalf("release journalled again: %v", j.commits)
	}
}

// Off, the commitment is journalled at the release, as before.
func TestCommitJournalEarlyOffJournalsAtRelease(t *testing.T) {
	defer SetCommitJournalEarlyForTest(false)()
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.SetTwoPhaseVote(true)
	j := &commitJournalRecorder{outputCh: outputCh}
	follower.SetVoteJournal(j)

	blockHash := types.Hash{0xC2}
	parentHash := types.Hash{0xA2}
	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, blockHash))
	if len(j.commits) != 0 {
		t.Fatalf("held commitment journalled with the switch off: %v", j.commits)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: parentHash}); err != nil {
		t.Fatal(err)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
		Hash: parentHash}); err != nil {
		t.Fatal(err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 1 {
		t.Fatalf("commit vote after attestation: %d, want 1", n)
	}
	if len(j.commits) != 1 {
		t.Fatalf("commitment journalled %v, want once at the release", j.commits)
	}
}

// A failed early write leaves the vote held unjournalled; the release then
// journals it and sends.
func TestCommitJournalEarlyFailedWriteJournalsAtRelease(t *testing.T) {
	defer SetCommitJournalEarlyForTest(true)()
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.SetTwoPhaseVote(true)
	j := &commitJournalRecorder{outputCh: outputCh, failCommit: true}
	follower.SetVoteJournal(j)

	blockHash := types.Hash{0xC3}
	parentHash := types.Hash{0xA3}
	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, blockHash))
	if follower.pendingCommitJournaled {
		t.Fatal("held vote marked journalled after a failed write")
	}
	j.failCommit = false
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: parentHash}); err != nil {
		t.Fatal(err)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
		Hash: parentHash}); err != nil {
		t.Fatal(err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 1 {
		t.Fatalf("commit vote after attestation: %d, want 1", n)
	}
	if len(j.commits) != 1 {
		t.Fatalf("commitment journalled %v, want once at the release", j.commits)
	}
}

// leaderToPrepareQC has leader 1 of 4 propose blockHash in view 1 and receive
// prepare votes from 0 and 2, which forms the PrepareQC.
func leaderToPrepareQC(t *testing.T, setup *testSetup, e *ConsensusEngine, outputCh chan EngineOutput, blockHash types.Hash) []EngineOutput {
	t.Helper()
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatal(err)
	}
	drainOutputs(outputCh)
	for _, i := range []int{0, 2} {
		vote := &Vote{View: 1, BlockHash: blockHash, Voter: ValidatorIndex(i),
			Signature: setup.keys[i].Sign(SigningMessage(1, blockHash)).Marshal()}
		if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage,
			Msg: ConsensusMsg{Type: MsgVote, Payload: vote}}); err != nil {
			t.Fatalf("vote from %d: %v", i, err)
		}
	}
	return drainOutputs(outputCh)
}

func sendCommitVotes(t *testing.T, setup *testSetup, e *ConsensusEngine, blockHash types.Hash, voters ...int) {
	t.Helper()
	for _, i := range voters {
		cv := &CommitVote{View: 1, BlockHash: blockHash, Voter: ValidatorIndex(i),
			Signature: setup.keys[i].Sign(CommitSigningMessage(1, blockHash)).Marshal()}
		if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage,
			Msg: ConsensusMsg{Type: MsgCommitVote, Payload: cv}}); err != nil {
			t.Fatalf("commit vote from %d: %v", i, err)
		}
	}
}

// The leader broadcasts the PrepareQC before journalling its own commitment,
// and its self vote still counts toward the CommitQC.
func TestCommitJournalEarlyLeaderBroadcastsFirst(t *testing.T) {
	for _, early := range []bool{false, true} {
		restore := SetCommitJournalEarlyForTest(early)
		setup := newTestSetup(t, 4)
		leader, outputCh := newTestEngine(t, setup, 1)
		j := &commitJournalRecorder{outputCh: outputCh}
		leader.SetVoteJournal(j)
		blockHash := types.Hash{0xC4}

		outs := leaderToPrepareQC(t, setup, leader, outputCh, blockHash)
		if countOutputs(outs, OutputBroadcast, MsgPrepareQC) != 1 {
			t.Fatalf("early=%v: PrepareQC not broadcast", early)
		}
		if len(j.commits) != 1 {
			t.Fatalf("early=%v: self commitment journalled %v", early, j.commits)
		}
		if queued := j.queuedAt[0] > 0; queued != early {
			t.Fatalf("early=%v: PrepareQC queued before the journal write = %v", early, queued)
		}
		// The self vote plus 0 and 2 make the quorum of 3.
		sendCommitVotes(t, setup, leader, blockHash, 0, 2)
		if countOutputs(drainOutputs(outputCh), OutputBlockCommitted, 0) == 0 {
			t.Fatalf("early=%v: no commit with the self vote and two others", early)
		}
		restore()
	}
}

// A leader whose commitment journal fails still broadcast the PrepareQC; it
// abstains in Round 2 and the followers' votes alone commit the block.
func TestCommitJournalEarlyLeaderJournalFailureAbstains(t *testing.T) {
	defer SetCommitJournalEarlyForTest(true)()
	setup := newTestSetup(t, 4)
	leader, outputCh := newTestEngine(t, setup, 1)
	j := &commitJournalRecorder{outputCh: outputCh, failCommit: true}
	leader.SetVoteJournal(j)
	blockHash := types.Hash{0xC5}

	if err := leader.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatal(err)
	}
	drainOutputs(outputCh)
	var outs []EngineOutput
	for _, i := range []int{0, 2} {
		vote := &Vote{View: 1, BlockHash: blockHash, Voter: ValidatorIndex(i),
			Signature: setup.keys[i].Sign(SigningMessage(1, blockHash)).Marshal()}
		_ = leader.ProcessEvent(ConsensusEvent{Type: EventMessage,
			Msg: ConsensusMsg{Type: MsgVote, Payload: vote}}) // the journal error surfaces here
	}
	outs = drainOutputs(outputCh)
	if countOutputs(outs, OutputBroadcast, MsgPrepareQC) != 1 {
		t.Fatal("PrepareQC not broadcast")
	}
	sendCommitVotes(t, setup, leader, blockHash, 0, 2)
	if countOutputs(drainOutputs(outputCh), OutputBlockCommitted, 0) != 0 {
		t.Fatal("committed with an unjournalled self vote counted")
	}
	sendCommitVotes(t, setup, leader, blockHash, 3)
	if countOutputs(drainOutputs(outputCh), OutputBlockCommitted, 0) == 0 {
		t.Fatal("followers' quorum did not commit")
	}
}
