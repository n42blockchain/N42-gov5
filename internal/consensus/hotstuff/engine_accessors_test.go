package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestEngine_RestoreAndStateAccessors covers the crash-recovery restore
// helpers and the small locked accessors around them.
func TestEngine_RestoreAndStateAccessors(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	lockedQC := QuorumCertificate{View: 3, BlockHash: types.Hash{0x01}}
	committedQC := QuorumCertificate{View: 2, BlockHash: types.Hash{0x02}}
	engine.RestoreState(ViewNumber(5), lockedQC, committedQC, 2)

	if engine.LockedQC().View != 3 {
		t.Fatalf("expected restored locked QC view 3, got %d", engine.LockedQC().View)
	}
	if engine.LastCommittedQC().View != 2 {
		t.Fatalf("expected restored committed QC view 2, got %d", engine.LastCommittedQC().View)
	}
	if engine.ConsecutiveTimeouts() != 2 {
		t.Fatalf("expected 2 consecutive timeouts, got %d", engine.ConsecutiveTimeouts())
	}

	engine.RestoreVoteCommitments(ViewNumber(5), types.Hash{0x03}, ViewNumber(4), types.Hash{0x04})

	if engine.ValidatorCount() != 4 {
		t.Fatalf("expected 4 validators, got %d", engine.ValidatorCount())
	}
	if em := engine.EpochManager(); em == nil {
		t.Fatalf("expected non-nil epoch manager")
	}
	if vs := engine.ValidatorSetForView(ViewNumber(5)); vs == nil || vs.Len() != 4 {
		t.Fatalf("ValidatorSetForView: expected 4-member set, got %+v", vs)
	}
	if vs := engine.ResolveQCValidatorSet(ViewNumber(5), 4); vs == nil {
		t.Fatalf("ResolveQCValidatorSet: expected non-nil set")
	}

	epoch, validators, _, ok := engine.CurrentEpochInfoSafe()
	if !ok || len(validators) != 4 {
		t.Fatalf("CurrentEpochInfoSafe: unexpected result epoch=%d validators=%d ok=%v", epoch, len(validators), ok)
	}
	if _, _, _, ok := engine.StagedEpochInfoSafe(); ok {
		t.Fatalf("StagedEpochInfoSafe: expected no staged epoch by default")
	}

	if rm := engine.ReconfigManager(); rm == nil {
		t.Fatalf("expected non-nil reconfig manager")
	}
}

// TestEngine_RestoreValidatorSetMembershipFlip covers both branches of
// RestoreValidatorSet: the restarting node still being a member, and it
// having been dropped from the set (falls back to observer/removed).
func TestEngine_RestoreValidatorSetMembershipFlip(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)
	engine.SetSelfAddress(setup.validators[0].Address)

	// Still a member: restore the same set, myIndex should resolve to 0.
	engine.RestoreValidatorSet(1, setup.validators, setup.f)
	if engine.IsRemoved() {
		t.Fatalf("expected not removed after restoring a set that still contains self")
	}

	// Dropped: restore a set without validator 0's address.
	engine.RestoreValidatorSet(2, setup.validators[1:], setup.f)
	if !engine.IsRemoved() {
		t.Fatalf("expected removed after restoring a set without self")
	}

	// RestoreStagedSet and PreStageFromScheduleSafe.
	engine.RestoreStagedSet(setup.validators, setup.f)
	_, _, _, ok := engine.StagedEpochInfoSafe()
	if !ok {
		t.Fatalf("expected a staged epoch after RestoreStagedSet")
	}

	sched := &EpochSchedule{}
	if engine.PreStageFromScheduleSafe(sched) {
		t.Fatalf("expected PreStageFromScheduleSafe to report no new staging for an empty schedule")
	}
}

// TestEngine_LeaderAndRemoval covers IsCurrentLeader/CurrentLeaderIndex and
// the SetRemoved/IsRemoved lifecycle.
func TestEngine_LeaderAndRemoval(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine0, _ := newTestEngine(t, setup, 0)

	leaderIdx := engine0.CurrentLeaderIndex()
	isLeader := engine0.IsCurrentLeader()
	if isLeader != (leaderIdx == ValidatorIndex(0)) {
		t.Fatalf("IsCurrentLeader inconsistent with CurrentLeaderIndex: leader=%d isLeader=%v", leaderIdx, isLeader)
	}

	if engine0.IsRemoved() {
		t.Fatalf("expected not removed initially")
	}
	engine0.SetRemoved()
	if !engine0.IsRemoved() {
		t.Fatalf("expected removed after SetRemoved")
	}
}

// TestIsCriticalOutput covers the critical/non-critical output-type
// classification used to decide log severity on a full output channel.
func TestIsCriticalOutput(t *testing.T) {
	critical := []EngineOutputType{
		OutputBlockCommitted, OutputBroadcast, OutputSendToValidator,
		OutputEquivocationDetected, OutputEpochTransition,
	}
	for _, c := range critical {
		if !isCriticalOutput(c) {
			t.Fatalf("expected %v to be classified critical", c)
		}
	}
	if isCriticalOutput(EngineOutputType(255)) {
		t.Fatalf("expected an unknown output type to be non-critical")
	}
}

// TestEngine_OnBlockRejected covers the withdraw-evidence path (checked
// block present, not imported) and the no-op path (hash never checked).
func TestEngine_OnBlockRejected(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	hash := types.Hash{0x07}
	// No-op: hash was never marked checked.
	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockRejected, Hash: hash}); err != nil {
		t.Fatalf("ProcessEvent(EventBlockRejected) no-op: unexpected error: %v", err)
	}

	// Mark it checked (and not imported), then reject it; evidence should be
	// withdrawn without panicking.
	engine.mu.Lock()
	engine.checkedBlocks[hash] = true
	engine.checkedFIFO = append(engine.checkedFIFO, hash)
	engine.importedParents[hash] = types.Hash{0x08}
	engine.mu.Unlock()

	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockRejected, Hash: hash}); err != nil {
		t.Fatalf("ProcessEvent(EventBlockRejected): unexpected error: %v", err)
	}

	engine.mu.Lock()
	_, stillChecked := engine.checkedBlocks[hash]
	_, stillHasParent := engine.importedParents[hash]
	engine.mu.Unlock()
	if stillChecked {
		t.Fatalf("expected checkedBlocks entry removed after rejection")
	}
	if stillHasParent {
		t.Fatalf("expected importedParents entry removed when block was not imported")
	}
}
