package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

// newStagedValidatorSet builds a fresh n-validator set with distinct
// addresses (offset away from newTestSetup's own 1..n addresses) so tests can
// simulate an epoch-boundary reconfiguration that adds or drops members.
func newStagedValidatorSet(t *testing.T, n int, addrOffset byte) []ValidatorInfo {
	t.Helper()
	validators := make([]ValidatorInfo, n)
	for i := 0; i < n; i++ {
		sk, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		var addr types.Address
		addr[0] = addrOffset + byte(i)
		validators[i] = ValidatorInfo{Address: addr, PublicKey: sk.PublicKey()}
	}
	return validators
}

// newEpochTestEngine builds a ConsensusEngine with epoch transitions enabled
// (epochLength=2, so view 3 is the first boundary) instead of the disabled
// EpochManager newTestEngine wires up.
func newEpochTestEngine(t *testing.T, setup *testSetup, myIndex int, epochLength uint64) (*ConsensusEngine, chan EngineOutput) {
	t.Helper()
	outputCh := make(chan EngineOutput, 256)
	em := NewEpochManagerWithLength(setup.vs, epochLength)
	engine := NewConsensusEngineWithEpochManager(
		ValidatorIndex(myIndex),
		setup.keys[myIndex],
		em,
		1000, 10000,
		outputCh,
	)
	engine.SetSelfAddress(setup.validators[myIndex].Address)
	return engine, outputCh
}

// TestAdvanceToViewActivatesStagedEpochKeepingMembership exercises the
// epoch-boundary branch of advanceToView where a staged next-epoch set
// activates and this node's address is still present in it (at a different
// index): myIndex must be re-derived from myAddr rather than left stale.
func TestAdvanceToViewActivatesStagedEpochKeepingMembership(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newEpochTestEngine(t, setup, 0, 2)

	// Stage a reordered set that still contains validator 0's address, but at
	// a different slot (index 2 instead of 0).
	staged := make([]ValidatorInfo, 4)
	staged[0] = setup.validators[1]
	staged[1] = setup.validators[2]
	staged[2] = setup.validators[0] // this node, moved
	staged[3] = setup.validators[3]
	engine.epochManager.StageNextEpoch(staged, 1)

	if err := engine.advanceToView(3); err != nil { // view 3 is the first boundary (epochLength=2)
		t.Fatalf("advanceToView: %v", err)
	}

	if engine.epochManager.CurrentEpoch() != 1 {
		t.Fatalf("CurrentEpoch = %d, want 1", engine.epochManager.CurrentEpoch())
	}
	if !engine.isMember() {
		t.Fatal("expected the node to remain an active member after reconfiguration")
	}
	if engine.myIndex != 2 {
		t.Fatalf("myIndex = %d, want 2 (re-derived from address)", engine.myIndex)
	}
	if engine.roundState.CurrentView() != 3 {
		t.Fatalf("CurrentView = %d, want 3", engine.roundState.CurrentView())
	}

	outputs := drainOutputs(outputCh)
	foundTransition := false
	for _, o := range outputs {
		if o.Type == OutputEpochTransition && o.NewEpoch == 1 {
			foundTransition = true
		}
	}
	if !foundTransition {
		t.Fatal("expected an OutputEpochTransition for the new epoch")
	}
}

// TestAdvanceToViewRemovesNodeAtEpochBoundary exercises the branch where the
// staged next-epoch set no longer contains this node's address: the node
// must emit OutputEpochTransition{Removed:true}, become a silent observer
// (NonMemberIndex) and stop its pacemaker.
//
// Note: the removal branch returns immediately after doing this, which skips
// the rest of advanceToView -- including e.roundState.AdvanceView(newView).
// This test pins that as the engine's actual, current behavior (the round
// state's CurrentView does NOT advance to the boundary view on removal); see
// the final report for why this looks like a latent defect that was not
// fixed here.
func TestAdvanceToViewRemovesNodeAtEpochBoundary(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newEpochTestEngine(t, setup, 0, 2)

	viewBeforeBoundary := engine.roundState.CurrentView()

	// Stage a replacement set that excludes validator 0's address entirely.
	staged := newStagedValidatorSet(t, 4, 200)
	engine.epochManager.StageNextEpoch(staged, 1)

	if err := engine.advanceToView(3); err != nil {
		t.Fatalf("advanceToView: %v", err)
	}

	if engine.isMember() {
		t.Fatal("expected the node to be removed (NonMemberIndex) after reconfiguration")
	}
	if engine.myIndex != NonMemberIndex {
		t.Fatalf("myIndex = %d, want NonMemberIndex", engine.myIndex)
	}
	if !engine.removed {
		t.Fatal("expected engine.removed to be true")
	}

	outputs := drainOutputs(outputCh)
	foundRemoval := false
	for _, o := range outputs {
		if o.Type == OutputEpochTransition && o.Removed {
			foundRemoval = true
		}
	}
	if !foundRemoval {
		t.Fatal("expected an OutputEpochTransition{Removed:true}")
	}

	// Documents current behavior: the removal branch returns before
	// AdvanceView runs, so CurrentView is left at its pre-boundary value.
	if engine.roundState.CurrentView() != viewBeforeBoundary {
		t.Fatalf("CurrentView = %d, want unchanged %d (removal short-circuits AdvanceView)",
			engine.roundState.CurrentView(), viewBeforeBoundary)
	}
}

// TestAdvanceToViewNoEpochTransitionWhenNothingStaged exercises the common
// path at an epoch boundary view with nothing staged: AdvanceEpoch must
// report no transition and the active epoch/validator set must be unchanged.
func TestAdvanceToViewNoEpochTransitionWhenNothingStaged(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newEpochTestEngine(t, setup, 0, 2)

	if err := engine.advanceToView(3); err != nil {
		t.Fatalf("advanceToView: %v", err)
	}
	if engine.epochManager.CurrentEpoch() != 0 {
		t.Fatalf("CurrentEpoch = %d, want 0 (no transition)", engine.epochManager.CurrentEpoch())
	}
	if engine.myIndex != 0 {
		t.Fatalf("myIndex = %d, want unchanged 0", engine.myIndex)
	}
	outputs := drainOutputs(outputCh)
	for _, o := range outputs {
		if o.Type == OutputEpochTransition {
			t.Fatal("expected no OutputEpochTransition when nothing is staged")
		}
	}
}
