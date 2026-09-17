package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// buildPrepareQCMsg makes a PrepareQC a follower will accept for view 1.
func buildPrepareQCMsg(t *testing.T, setup *testSetup, blockHash types.Hash) *PrepareQCMsg {
	t.Helper()
	prepareMsg := SigningMessage(1, blockHash)
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())
	for i := 0; i < 3; i++ {
		_ = vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(prepareMsg))
	}
	qc, err := vc.BuildQC(setup.vs)
	if err != nil {
		t.Fatal(err)
	}
	return &PrepareQCMsg{View: 1, BlockHash: blockHash, QC: *qc}
}

func deliverPrepareQC(t *testing.T, e *ConsensusEngine, msg *PrepareQCMsg) {
	t.Helper()
	if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgPrepareQC, Payload: msg}}); err != nil {
		t.Fatalf("PrepareQC: %v", err)
	}
}

// TestTwoPhaseCommitVoteOnDeferredAttestation: under deferred execution the
// Round-2 guarantee is "this node checked the block and executed its parent",
// not "this node executed the block". Holding the commit vote for the block's
// own import is what kept round 35zzq at the same 1.33 s block time as the
// round without deferred execution.
func TestTwoPhaseCommitVoteOnDeferredAttestation(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.SetTwoPhaseVote(true)

	blockHash := types.Hash{0xB1}
	parentHash := types.Hash{0xA1}

	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, blockHash))
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote before any attestation: %d", n)
	}

	// Checked, but the parent is not imported yet: still held.
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: parentHash}); err != nil {
		t.Fatalf("EventBlockChecked: %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote with the parent unimported: %d", n)
	}

	// The parent imports: the guarantee is complete, the held vote fires.
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
		Hash: parentHash}); err != nil {
		t.Fatalf("EventBlockImported(parent): %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 1 {
		t.Fatalf("commit vote after the parent imported: %d, want 1", n)
	}
}

// The same guarantee arriving in the other order: the parent is imported first
// and the check lands second.
func TestTwoPhaseCommitVoteWhenCheckArrivesAfterParent(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.SetTwoPhaseVote(true)

	blockHash := types.Hash{0xB2}
	parentHash := types.Hash{0xA2}

	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
		Hash: parentHash}); err != nil {
		t.Fatalf("EventBlockImported(parent): %v", err)
	}
	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, blockHash))
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote before the check: %d", n)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
		Hash: blockHash, ParentHash: parentHash}); err != nil {
		t.Fatalf("EventBlockChecked: %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 1 {
		t.Fatalf("commit vote after the check: %d, want 1", n)
	}
}

// Without deferred execution nothing changes: the vote still waits for this
// node to import the block itself.
func TestTwoPhaseCommitVoteStillWaitsForImportWithoutDeferred(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.SetTwoPhaseVote(true)

	blockHash := types.Hash{0xB3}
	parentHash := types.Hash{0xA3}

	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, blockHash))
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
		Hash: parentHash}); err != nil {
		t.Fatalf("EventBlockImported(parent): %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("an unchecked block commit-voted on its parent's import: %d", n)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
		Hash: blockHash, ParentHash: parentHash}); err != nil {
		t.Fatalf("EventBlockImported(block): %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 1 {
		t.Fatalf("commit vote after the block imported: %d, want 1", n)
	}
}
