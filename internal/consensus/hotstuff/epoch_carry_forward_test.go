// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/syncanchor"
)

// Fleet shape: epochLength 200, a static validator set, no epoch schedule.
// View 24514 is in epoch 122 (views 24401..24600); 24766 is in epoch 123.
const (
	carryEpochLen  = 200
	carrySeedView  = ViewNumber(24514)
	carryJumpView  = ViewNumber(24766)
	carrySeedEpoch = uint64(122)
)

// restoredEpochEngine is a restarted validator: RestoreState seeds the epoch
// counter from the persisted view.
func restoredEpochEngine(t *testing.T, setup *testSetup) (*ConsensusEngine, chan EngineOutput) {
	t.Helper()
	engine, outputCh := newEpochTestEngine(t, setup, 0, carryEpochLen)
	engine.RestoreState(carrySeedView, QuorumCertificate{}, QuorumCertificate{}, 0)
	if got := engine.epochManager.CurrentEpoch(); got != carrySeedEpoch {
		t.Fatalf("seeded epoch = %d, want %d", got, carrySeedEpoch)
	}
	return engine, outputCh
}

// A view jump across a boundary on a static-set chain must carry the set into
// the new epoch, so strict sync verification knows that epoch.
func TestEpochCarryForwardStaticSetViewJump(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := restoredEpochEngine(t, setup)

	if err := engine.advanceToView(carryJumpView); err != nil {
		t.Fatalf("advanceToView: %v", err)
	}
	if got := engine.epochManager.CurrentEpoch(); got != 123 {
		t.Fatalf("CurrentEpoch = %d, want 123", got)
	}
	vs, epoch, ok := engine.SyncCommitSetForView(24700)
	if !ok || epoch != 123 || vs != setup.vs {
		t.Fatalf("SyncCommitSetForView(24700) = (%v, %d, %v), want the static set in epoch 123", vs, epoch, ok)
	}
	if vs, epoch, ok := engine.SyncCommitSetForView(24514); !ok || epoch != carrySeedEpoch || vs != setup.vs {
		t.Fatalf("seed epoch lost: (%v, %d, %v)", vs, epoch, ok)
	}
	// One epoch past the current one, with nothing staged, stays unknown.
	if _, epoch, ok := engine.SyncCommitSetForView(24900); ok || epoch != 124 {
		t.Fatalf("epoch 124 must fail closed: (%d, %v)", epoch, ok)
	}
	if engine.myIndex != 0 {
		t.Fatalf("myIndex = %d, want 0", engine.myIndex)
	}
	for _, o := range drainOutputs(outputCh) {
		if o.Type == OutputEpochTransition || o.Type == OutputEpochStaged {
			t.Fatalf("carry-forward must not report a set change: %+v", o)
		}
	}
}

// A jump over several boundaries advances once per boundary; every crossed
// epoch resolves to the carried set.
func TestEpochCarryForwardMultiBoundaryJump(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := restoredEpochEngine(t, setup)

	const target = ViewNumber(25555) // epoch 127
	if err := engine.advanceToView(target); err != nil {
		t.Fatalf("advanceToView: %v", err)
	}
	if got := engine.epochManager.CurrentEpoch(); got != 127 {
		t.Fatalf("CurrentEpoch = %d, want 127", got)
	}
	for epoch := carrySeedEpoch; epoch <= 127; epoch++ {
		view := ViewNumber(epoch*carryEpochLen + 50)
		vs, got, ok := engine.SyncCommitSetForView(view)
		if !ok || got != epoch || vs != setup.vs {
			t.Fatalf("epoch %d (view %d): (%v, %d, %v)", epoch, view, vs, got, ok)
		}
	}
	if _, _, ok := engine.SyncCommitSetForView(ViewNumber(128*carryEpochLen + 50)); ok {
		t.Fatal("epoch 128 must fail closed")
	}
}

// A staged set still activates at the first boundary it belongs to, also when
// a view jump skips past that boundary; later boundaries carry it forward.
func TestEpochCarryForwardStagedSetActivatesAtFirstBoundary(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := restoredEpochEngine(t, setup)
	staged := newStagedValidatorSet(t, 7, 0x40)
	staged[3] = setup.validators[0] // this node stays, at index 3
	engine.epochManager.StageNextEpoch(staged, 2)

	const target = ViewNumber(24950) // epoch 124: boundaries 24601 and 24801
	if err := engine.advanceToView(target); err != nil {
		t.Fatalf("advanceToView: %v", err)
	}
	em := engine.epochManager
	if em.CurrentEpoch() != 124 || em.CurrentValidatorSet().Len() != 7 || em.HasStagedNext() {
		t.Fatalf("after jump: epoch %d, set %d, staged %v", em.CurrentEpoch(), em.CurrentValidatorSet().Len(), em.HasStagedNext())
	}
	if engine.myIndex != 3 {
		t.Fatalf("myIndex = %d, want 3", engine.myIndex)
	}
	if em.setSinceView != 24601 {
		t.Fatalf("set took effect at view %d, want 24601", em.setSinceView)
	}
	for _, c := range []struct {
		view ViewNumber
		size uint32
	}{{24514, 4}, {24600, 4}, {24601, 7}, {24800, 7}, {24801, 7}} {
		vs, _, ok := engine.SyncCommitSetForView(c.view)
		if !ok || vs.Len() != c.size {
			t.Fatalf("view %d: ok=%v size=%v, want %d", c.view, ok, vs, c.size)
		}
	}
	transitions := 0
	for _, o := range drainOutputs(outputCh) {
		if o.Type == OutputEpochTransition {
			transitions++
			if o.NewEpoch != 123 {
				t.Fatalf("transition reported for epoch %d, want 123", o.NewEpoch)
			}
		}
	}
	if transitions != 1 {
		t.Fatalf("epoch transitions = %d, want 1", transitions)
	}
}

// Landing exactly on a boundary behaves as before: the staged set activates
// there.
func TestEpochCarryForwardStagedSetExactBoundary(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := restoredEpochEngine(t, setup)
	engine.epochManager.StageNextEpoch(newStagedValidatorSet(t, 7, 0x40), 2)
	if err := engine.advanceToView(24600); err != nil {
		t.Fatal(err)
	}
	if engine.epochManager.CurrentEpoch() != carrySeedEpoch || !engine.epochManager.HasStagedNext() {
		t.Fatal("staged set must wait for its boundary")
	}
	if err := engine.advanceToView(24601); err != nil {
		t.Fatal(err)
	}
	if em := engine.epochManager; em.CurrentEpoch() != 123 || em.CurrentValidatorSet().Len() != 7 {
		t.Fatalf("at boundary: epoch %d, set %d", em.CurrentEpoch(), em.CurrentValidatorSet().Len())
	}
}

// AdvanceEpoch with nothing staged carries the set and still advances the
// epoch counter; it reports no set change.
func TestAdvanceEpochCarriesSetWhenNothingStaged(t *testing.T) {
	setup := newTestSetup(t, 4)
	em := NewEpochManagerWithLength(setup.vs, carryEpochLen)
	if em.AdvanceEpoch(201) {
		t.Fatal("no staged set: AdvanceEpoch must not report a change")
	}
	if em.CurrentEpoch() != 1 || em.CurrentValidatorSet() != setup.vs {
		t.Fatalf("epoch %d set %p", em.CurrentEpoch(), em.CurrentValidatorSet())
	}
	for _, epoch := range []uint64{0, 1} {
		if vs, ok := em.StrictSetForEpoch(epoch); !ok || vs != setup.vs {
			t.Fatalf("epoch %d unknown after carry-forward", epoch)
		}
	}
	if _, ok := em.StrictSetForEpoch(2); ok {
		t.Fatal("epoch 2 must fail closed")
	}
}

// A restored staged set whose boundary the persisted view already passed is
// activated by RestoreState, at that boundary.
func TestEpochCarryForwardRestoreStateActivatesPassedStagedSet(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newEpochTestEngine(t, setup, 0, carryEpochLen)
	engine.RestoreValidatorSet(carrySeedEpoch, setup.validators, setup.f)
	staged := newStagedValidatorSet(t, 7, 0x40)
	staged[5] = setup.validators[0]
	engine.RestoreStagedSet(staged, 2)

	engine.RestoreState(carryJumpView, QuorumCertificate{}, QuorumCertificate{}, 0)
	em := engine.epochManager
	if em.CurrentEpoch() != 123 || em.CurrentValidatorSet().Len() != 7 || em.HasStagedNext() {
		t.Fatalf("after restore: epoch %d, set %d, staged %v", em.CurrentEpoch(), em.CurrentValidatorSet().Len(), em.HasStagedNext())
	}
	if engine.myIndex != 5 {
		t.Fatalf("myIndex = %d, want 5", engine.myIndex)
	}
	if vs, _, ok := engine.SyncCommitSetForView(24550); !ok || vs.Len() != 4 {
		t.Fatal("epoch 122 must still resolve to the replaced set")
	}
}

// End to end through the strict sync verifier: a QC jump by message (as a
// restarted node experiences it) makes the next epoch's header QCs anchors.
func TestSyncAnchorVerifierAfterEpochViewJump(t *testing.T) {
	setup := newTestSetup(t, 4)
	s, ce := syncAnchorService(t, setup)
	em := NewEpochManagerWithLength(setup.vs, carryEpochLen)
	ce.mu.Lock()
	ce.epochManager = em
	ce.mu.Unlock()
	ce.RestoreState(carrySeedView, QuorumCertificate{}, QuorumCertificate{}, 0)

	jumpHash := types.HexToHash("0x24765")
	jumpQC := signedQC(t, setup, carryJumpView-1, jumpHash, ce.voteSigningMessage(carryJumpView-1, jumpHash))
	if err := ce.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{
		Type:    MsgPrepareQC,
		Payload: &PrepareQCMsg{View: carryJumpView + 30, BlockHash: jumpHash, QC: *jumpQC},
	}}); err != nil {
		t.Fatalf("view jump: %v", err)
	}
	if ce.CurrentView() != carryJumpView {
		t.Fatalf("view = %d, want %d", ce.CurrentView(), carryJumpView)
	}

	hash := types.HexToHash("0x24700")
	extra := headerExtraFor(t, 24702, signedQC(t, setup, 24700, hash, ce.commitSigningMessage(24700, hash)))
	if r := s.VerifyHeaderQC(extra); r.Verdict != syncanchor.Verified || r.Epoch != 123 {
		t.Fatalf("epoch-123 QC after the jump: %+v", r)
	}
}

// A negative verdict against a carried-forward set is not cached: the set may
// still turn out to have changed for that epoch.
func TestSyncAnchorCarriedSetNegativeVerdictNotCached(t *testing.T) {
	setup := newTestSetup(t, 4)
	s, ce := syncAnchorService(t, setup)
	em := NewEpochManagerWithLength(setup.vs, carryEpochLen)
	ce.mu.Lock()
	ce.epochManager = em
	ce.mu.Unlock()
	ce.RestoreState(carrySeedView, QuorumCertificate{}, QuorumCertificate{}, 0)
	ce.mu.Lock()
	if err := ce.advanceToView(carryJumpView); err != nil {
		ce.mu.Unlock()
		t.Fatal(err)
	}
	ce.mu.Unlock()

	other := newTestSetup(t, 4)
	hash := types.HexToHash("0x24701")
	extra := headerExtraFor(t, 24703, signedQC(t, other, 24701, hash, ce.commitSigningMessage(24701, hash)))
	for i := 0; i < 2; i++ {
		r := s.VerifyHeaderQC(extra)
		if r.Verdict != syncanchor.BadQC || r.Cached {
			t.Fatalf("attempt %d: %+v, want an uncached BadQC", i, r)
		}
	}
}
