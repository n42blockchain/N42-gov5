package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestProcessPrepareQCRejectsViewMismatch exercises the early view-check
// guard: a PrepareQC for a view other than the engine's current view must be
// rejected with a *ViewMismatchError and must not advance any vote state.
func TestProcessPrepareQCRejectsViewMismatch(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)

	blockHash := types.Hash{0xC1}
	msg := buildPrepareQCMsg(t, setup, blockHash)
	msg.View = follower.roundState.CurrentView() + 1 // mismatched

	err := follower.processPrepareQC(msg, msgTiming{})
	if err == nil {
		t.Fatal("expected a view mismatch error")
	}
	if _, ok := err.(*ViewMismatchError); !ok {
		t.Fatalf("expected *ViewMismatchError, got %T: %v", err, err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("expected no commit vote, got %d", n)
	}
}

// TestProcessPrepareQCSuppressesDuplicateCommitVote exercises the
// double-vote prevention guard: once a commit vote has been cast for a view,
// a second PrepareQC for the same view (e.g. a retransmit) must be a no-op.
func TestProcessPrepareQCSuppressesDuplicateCommitVote(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)

	blockHash := types.Hash{0xC2}
	msg := buildPrepareQCMsg(t, setup, blockHash)

	if err := follower.processPrepareQC(msg, msgTiming{}); err != nil {
		t.Fatalf("first processPrepareQC: %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 1 {
		t.Fatalf("expected exactly one commit vote on first delivery, got %d", n)
	}

	if err := follower.processPrepareQC(msg, msgTiming{}); err != nil {
		t.Fatalf("second (duplicate) processPrepareQC: %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("expected no additional commit vote on retransmit, got %d", n)
	}
}

// TestProcessPrepareQCRefusesWhenNotExtendingJustify exercises the S26
// defense-in-depth branch: when the block's known parent does not match the
// view's recorded JustifyQC block, the commit vote must be refused silently
// (no error, no vote).
func TestProcessPrepareQCRefusesWhenNotExtendingJustify(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)

	blockHash := types.Hash{0xC3}
	actualParent := types.Hash{0xAA}
	justifyBlock := types.Hash{0xBB} // deliberately different from actualParent

	view := follower.roundState.CurrentView()
	follower.pendingJustifyBlocks[view] = justifyBlock
	follower.importedParents[blockHash] = actualParent

	msg := buildPrepareQCMsg(t, setup, blockHash)
	if err := follower.processPrepareQC(msg, msgTiming{}); err != nil {
		t.Fatalf("processPrepareQC: %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("expected the commit vote to be refused, got %d", n)
	}
	if follower.roundState.HasCommitVotedInView(view) {
		t.Fatal("expected HasCommitVotedInView to remain false after a refusal")
	}
}

// TestProcessPrepareQCObserverDoesNotVote exercises the isMember() guard: a
// node whose index has been reset to NonMemberIndex (observer/removed) must
// not cast a Round 2 commit vote even for an otherwise-valid PrepareQC.
func TestProcessPrepareQCObserverDoesNotVote(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	follower.myIndex = NonMemberIndex

	blockHash := types.Hash{0xC4}
	msg := buildPrepareQCMsg(t, setup, blockHash)

	if err := follower.processPrepareQC(msg, msgTiming{}); err != nil {
		t.Fatalf("processPrepareQC: %v", err)
	}
	if n := countOutputs(drainOutputs(outputCh), OutputSendToValidator, MsgCommitVote); n != 0 {
		t.Fatalf("expected an observer to cast no commit vote, got %d", n)
	}
}
