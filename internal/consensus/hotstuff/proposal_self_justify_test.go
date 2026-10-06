// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// S34 (docs/OPEN_ISSUES.md, the stale-re-proposal item; docs/QS_BLOCK_TIME_BUDGET.md
// 6dj/6dk): round 35zzzk view 6557 -- a leader re-proposed its own
// already-committed block (whose QC was, by then, the highest known) as if
// it were a fresh candidate. justifyQC ended up equal to the proposed
// block's own hash: a proposal that certifies nothing (a block cannot
// extend itself), which every voter correctly refused (extendsJustify,
// "import-gated vote REFUSED"), costing one view's timeout for no reason.
// This test pins the guard added to onBlockReady: it must never broadcast a
// Proposal whose BlockHash equals its own JustifyQC.BlockHash, regardless of
// whether importedParents happens to know anything about it (it never does
// for a leader's own sealed block -- see the guard's own comment).
func TestSealedBlockDroppedWhenItWouldJustifyItself(t *testing.T) {
	engine, out := singleValidatorEngine(t)

	committedBlock := types.Hash{0xA9} // stand-in for a990bc.., already committed
	engine.roundState.UpdateLockedQC(&QuorumCertificate{View: 5, BlockHash: committedBlock})

	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: committedBlock}); err != nil {
		t.Fatalf("onBlockReady returned an error; it should drop quietly and let the view time out: %v", err)
	}
	if broadcastsProposal(drainOutputs(out)) {
		t.Fatal("proposed a block equal to its own JustifyQC block; no voter can accept a block that extends itself")
	}
	if engine.roundState.Phase() != PhaseWaitingForProposal {
		t.Fatal("a rejected self-justifying proposal must leave the phase at WaitingForProposal, " +
			"so a genuinely fresh seal for this same view can still be proposed")
	}
}

// The exact incident, end to end: block X is committed (LockedQC now
// certifies X). The leader of the NEXT view has two seal-completion events
// in flight: (1) the stale sibling-suppression path re-proposing X itself
// (worker.go's "kept" block, already committed and written), and (2) the
// genuinely fresh seal for X's own child, Y. The Proposal that goes out for
// this view must be Y -- never X itself, never any other stale sibling --
// and (1) arriving first must not prevent (2) from succeeding.
func TestFreshSealStillProposedAfterAStaleSelfJustifyAttempt(t *testing.T) {
	engine, out := singleValidatorEngine(t)

	blockX := types.Hash{0xA9} // already committed
	blockY := types.Hash{0xB1} // fresh child of X, the correct next block

	engine.roundState.UpdateLockedQC(&QuorumCertificate{View: 5, BlockHash: blockX})
	engine.rememberImported(blockY, blockX) // Y's parent is X, positively known

	// (1) The stale re-proposal attempt arrives first, exactly as it did live.
	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockX}); err != nil {
		t.Fatalf("onBlockReady(X): %v", err)
	}
	if broadcastsProposal(drainOutputs(out)) {
		t.Fatal("the stale re-proposal of the already-committed block must not produce a Proposal")
	}

	// (2) The fresh, correct seal arrives moments later, same view.
	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockY}); err != nil {
		t.Fatalf("onBlockReady(Y): %v", err)
	}
	outputs := drainOutputs(out)
	if !broadcastsProposal(outputs) {
		t.Fatal("the fresh, correct seal was dropped instead of proposed -- " +
			"the stale attempt must not consume the WaitingForProposal phase")
	}
	for _, o := range outputs {
		if o.Type != OutputBroadcast || o.Message == nil || o.Message.Type != MsgProposal {
			continue
		}
		p, ok := o.Message.Payload.(*Proposal)
		if !ok {
			t.Fatalf("MsgProposal payload was not *Proposal: %T", o.Message.Payload)
		}
		if p.BlockHash != blockY {
			t.Fatalf("proposed %x, want the fresh block %x (never X, never any stale sibling)", p.BlockHash, blockY)
		}
	}
}

// Sanity: the guard must not fire on the ordinary case, where the proposed
// block's own hash genuinely differs from the current JustifyQC block.
func TestSealedBlockProposedWhenNotSelfJustify(t *testing.T) {
	engine, out := singleValidatorEngine(t)

	blockHash := types.Hash{0xAB}
	justify := types.Hash{0x11}
	engine.roundState.UpdateLockedQC(&QuorumCertificate{View: 5, BlockHash: justify})
	engine.rememberImported(blockHash, justify)

	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatalf("onBlockReady: %v", err)
	}
	if !broadcastsProposal(drainOutputs(out)) {
		t.Fatal("dropped a normal, non-self-justifying proposal")
	}
}
