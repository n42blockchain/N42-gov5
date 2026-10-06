package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// deferredRound drives one HotStuff-2 round the way deferred execution runs on
// the fleet: the followers never import the block they are voting on. They
// receive the proposal, report it CHECKED against the parent (its header
// carries their own result of the parent, its transactions are includable),
// and they have imported the PARENT. attest=false models the same round
// without deferred execution evidence. Returns whether the leader committed.
func deferredRound(t *testing.T, h *chaosHarness, view ViewNumber, blockHash, parentHash types.Hash, attest bool) bool {
	t.Helper()
	n := len(h.engines)
	leader := int(view % uint64(n))

	if err := h.engines[leader].ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatalf("view %d: leader propose: %v", view, err)
	}
	var proposal *Proposal
	for _, o := range drainOutputs(h.channels[leader]) {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgProposal {
			proposal = o.Message.Payload.(*Proposal)
		}
	}
	if proposal == nil {
		t.Fatalf("view %d: no proposal", view)
	}

	for i := 0; i < n; i++ {
		if i == leader {
			continue
		}
		if err := h.engines[i].ProcessEvent(ConsensusEvent{Type: EventMessage,
			Msg: ConsensusMsg{Type: MsgProposal, Payload: proposal}}); err != nil {
			t.Fatalf("view %d node %d proposal: %v", view, i, err)
		}
		if attest {
			// The deferred check passed; the block itself is NOT imported.
			if err := h.engines[i].ProcessEvent(ConsensusEvent{Type: EventBlockChecked,
				Hash: blockHash, ParentHash: parentHash}); err != nil {
				t.Fatalf("view %d node %d checked: %v", view, i, err)
			}
		}
	}

	route := func(msgType ConsensusMsgType) {
		for i := 0; i < n; i++ {
			if i == leader {
				continue
			}
			for _, o := range drainOutputs(h.channels[i]) {
				if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == msgType {
					if err := h.engines[leader].ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: *o.Message}); err != nil {
						t.Fatalf("view %d leader %v from %d: %v", view, msgType, i, err)
					}
				}
			}
		}
	}
	route(MsgVote)

	var prepareQC *PrepareQCMsg
	for _, o := range drainOutputs(h.channels[leader]) {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgPrepareQC {
			prepareQC = o.Message.Payload.(*PrepareQCMsg)
		}
	}
	if prepareQC == nil {
		return false // no quorum of prepare votes
	}
	for i := 0; i < n; i++ {
		if i == leader {
			continue
		}
		if err := h.engines[i].ProcessEvent(ConsensusEvent{Type: EventMessage,
			Msg: ConsensusMsg{Type: MsgPrepareQC, Payload: prepareQC}}); err != nil {
			t.Fatalf("view %d node %d prepareQC: %v", view, i, err)
		}
	}
	route(MsgCommitVote)

	committed := false
	var decide *Decide
	for _, o := range drainOutputs(h.channels[leader]) {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgDecide {
			decide = o.Message.Payload.(*Decide)
		}
		if o.Type == OutputBlockCommitted {
			committed = true
		}
	}
	if decide != nil {
		for i := 0; i < n; i++ {
			if i == leader {
				continue
			}
			if err := h.engines[i].ProcessEvent(ConsensusEvent{Type: EventMessage,
				Msg: ConsensusMsg{Type: MsgDecide, Payload: decide}}); err != nil {
				t.Fatalf("view %d node %d decide: %v", view, i, err)
			}
			drainOutputs(h.channels[i])
		}
	}
	return committed
}

func newDeferredHarness(t *testing.T, genesis types.Hash) *chaosHarness {
	t.Helper()
	h := newChaosHarness(t, 7)
	for _, e := range h.engines {
		e.SetTwoPhaseVote(true)
	}
	h.markBlockImported(genesis)
	for i := range h.channels {
		drainOutputs(h.channels[i])
	}
	return h
}

// TestDeferredPipelineCommitsWithoutImportingTheBlock is the property the
// fleet round is supposed to measure: with deferred execution a block commits
// while every follower is still executing its parent. Round 35zzq showed the
// opposite on the fleet -- the Round-2 gate waited for each node to import the
// block itself, so the cycle stayed as long as an import.
func TestDeferredPipelineCommitsWithoutImportingTheBlock(t *testing.T) {
	genesis := types.Hash{0x60}
	h := newDeferredHarness(t, genesis)

	parent := genesis
	commits := 0
	for view := ViewNumber(1); view <= 3; view++ {
		blk := blockHashForView(view)
		if !deferredRound(t, h, view, blk, parent, true) {
			t.Fatalf("view %d did not commit on the deferred attestation alone", view)
		}
		commits++
		// Only now does the block execute anywhere -- a view behind consensus.
		h.markBlockImported(blk)
		for i := range h.channels {
			drainOutputs(h.channels[i])
		}
		parent = blk
	}
	if commits != 3 {
		t.Fatalf("committed %d blocks, want 3", commits)
	}
}

// The control: without the deferred evidence the same round does not commit,
// because the Round-2 gate is still waiting for the block's own import.
func TestWithoutDeferredEvidenceTheRoundWaitsForTheImport(t *testing.T) {
	genesis := types.Hash{0x61}
	h := newDeferredHarness(t, genesis)

	blk := blockHashForView(1)
	if deferredRound(t, h, 1, blk, genesis, false) {
		t.Fatal("a round committed with neither the block imported nor checked")
	}
}
