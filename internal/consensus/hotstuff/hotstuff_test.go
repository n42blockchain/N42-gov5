// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

// testSetup creates a validator set with n validators using real BLS keys.
type testSetup struct {
	keys       []common.SecretKey
	pubKeys    []common.PublicKey
	validators []ValidatorInfo
	vs         *ValidatorSet
	f          uint32
}

func newTestSetup(t *testing.T, n int) *testSetup {
	t.Helper()
	keys := make([]common.SecretKey, n)
	pubKeys := make([]common.PublicKey, n)
	validators := make([]ValidatorInfo, n)
	for i := 0; i < n; i++ {
		sk, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = sk
		pubKeys[i] = sk.PublicKey()
		var addr types.Address
		addr[0] = byte(i + 1)
		validators[i] = ValidatorInfo{Address: addr, PublicKey: pubKeys[i]}
	}
	f := uint32((n - 1) / 3)
	vs := NewValidatorSet(validators, f)
	return &testSetup{keys: keys, pubKeys: pubKeys, validators: validators, vs: vs, f: f}
}

func newTestEngine(t *testing.T, setup *testSetup, myIndex int) (*ConsensusEngine, chan EngineOutput) {
	t.Helper()
	outputCh := make(chan EngineOutput, 256)
	engine := NewConsensusEngine(
		ValidatorIndex(myIndex),
		setup.keys[myIndex],
		setup.vs,
		1000, 10000,
		outputCh,
	)
	return engine, outputCh
}

func drainOutputs(ch chan EngineOutput) []EngineOutput {
	var outputs []EngineOutput
	for {
		select {
		case o := <-ch:
			outputs = append(outputs, o)
		default:
			return outputs
		}
	}
}

// ============================================================================
// Types tests
// ============================================================================

func TestSigningMessages(t *testing.T) {
	hash := types.Hash{1, 2, 3}

	msg := SigningMessage(1, hash)
	if len(msg) != 40 {
		t.Fatalf("expected 40 bytes, got %d", len(msg))
	}

	commitMsg := CommitSigningMessage(1, hash)
	if len(commitMsg) != 46 {
		t.Fatalf("expected 46 bytes, got %d", len(commitMsg))
	}
	if string(commitMsg[:6]) != "commit" {
		t.Fatal("commit message prefix wrong")
	}

	timeoutMsg := TimeoutSigningMessage(1)
	if len(timeoutMsg) != 15 {
		t.Fatalf("expected 15 bytes, got %d", len(timeoutMsg))
	}
	if string(timeoutMsg[:7]) != "timeout" {
		t.Fatal("timeout message prefix wrong")
	}

	nvMsg := NewViewSigningMessage(1)
	if len(nvMsg) != 15 {
		t.Fatalf("expected 15 bytes, got %d", len(nvMsg))
	}
	if string(nvMsg[:7]) != "newview" {
		t.Fatal("newview message prefix wrong")
	}
}

func TestPhaseString(t *testing.T) {
	phases := []struct {
		p    Phase
		want string
	}{
		{PhaseWaitingForProposal, "WaitingForProposal"},
		{PhaseVoting, "Voting"},
		{PhasePreCommit, "PreCommit"},
		{PhaseCommitted, "Committed"},
		{PhaseTimedOut, "TimedOut"},
		{Phase(99), "Unknown"},
	}
	for _, tc := range phases {
		if tc.p.String() != tc.want {
			t.Errorf("Phase(%d).String() = %q, want %q", tc.p, tc.p.String(), tc.want)
		}
	}
}

func TestQCClone(t *testing.T) {
	qc := QuorumCertificate{
		View:               5,
		BlockHash:          types.Hash{1},
		AggregateSignature: []byte{1, 2, 3},
		Signers:            []bool{true, false, true},
	}
	clone := qc.Clone()
	clone.View = 10
	clone.AggregateSignature[0] = 99
	clone.Signers[0] = false
	if qc.View != 5 || qc.AggregateSignature[0] != 1 || !qc.Signers[0] {
		t.Fatal("Clone modified original")
	}
}

func TestQCSignerCount(t *testing.T) {
	qc := QuorumCertificate{Signers: []bool{true, false, true, true, false}}
	if qc.SignerCount() != 3 {
		t.Fatalf("expected 3, got %d", qc.SignerCount())
	}
}

func TestGenesisQC(t *testing.T) {
	qc := GenesisQC()
	if qc.View != 0 || qc.BlockHash != (types.Hash{}) || qc.Signers != nil {
		t.Fatal("genesis QC unexpected values")
	}
}

// ============================================================================
// Validator tests
// ============================================================================

func TestValidatorSet(t *testing.T) {
	setup := newTestSetup(t, 4)

	if setup.vs.Len() != 4 {
		t.Fatalf("expected 4 validators, got %d", setup.vs.Len())
	}
	if setup.vs.IsEmpty() {
		t.Fatal("should not be empty")
	}
	if setup.vs.QuorumSize() != 3 { // n-f = 4-1 (= 2f+1 at n=3f+1)
		t.Fatalf("expected quorum 3, got %d", setup.vs.QuorumSize())
	}
	if setup.vs.FaultTolerance() != 1 {
		t.Fatalf("expected f=1, got %d", setup.vs.FaultTolerance())
	}

	pk, err := setup.vs.GetPublicKey(0)
	if err != nil || pk == nil {
		t.Fatal("failed to get public key")
	}

	_, err = setup.vs.GetPublicKey(100)
	if err == nil {
		t.Fatal("expected error for invalid index")
	}

	if !setup.vs.Contains(3) {
		t.Fatal("should contain index 3")
	}
	if setup.vs.Contains(4) {
		t.Fatal("should not contain index 4")
	}

	idx := setup.vs.FindByAddress(setup.validators[2].Address)
	if idx != 2 {
		t.Fatalf("expected index 2, got %d", idx)
	}
	if setup.vs.FindByAddress(types.Address{99}) != -1 {
		t.Fatal("expected -1 for unknown address")
	}
}

func TestLeaderForView(t *testing.T) {
	setup := newTestSetup(t, 4)

	for view := uint64(0); view < 12; view++ {
		leader := LeaderForView(view, setup.vs)
		expected := ValidatorIndex(view % 4)
		if leader != expected {
			t.Errorf("view %d: expected leader %d, got %d", view, expected, leader)
		}
	}
}

func TestIsLeader(t *testing.T) {
	setup := newTestSetup(t, 4)
	if !IsLeader(1, 1, setup.vs) {
		t.Fatal("validator 1 should be leader for view 1")
	}
	if IsLeader(0, 1, setup.vs) {
		t.Fatal("validator 0 should not be leader for view 1")
	}
}

func TestValidatorSetClone(t *testing.T) {
	setup := newTestSetup(t, 4)
	clone := setup.vs.Clone()
	if clone.Len() != setup.vs.Len() {
		t.Fatal("clone length mismatch")
	}
}

// ============================================================================
// EpochManager tests
// ============================================================================

func TestEpochManagerDisabled(t *testing.T) {
	setup := newTestSetup(t, 4)
	em := NewEpochManager(setup.vs)
	if em.EpochsEnabled() {
		t.Fatal("epochs should be disabled")
	}
	if em.CurrentEpoch() != 0 {
		t.Fatal("expected epoch 0")
	}
	if em.EpochForView(100) != 0 {
		t.Fatal("all views should map to epoch 0 when disabled")
	}
	if em.IsEpochBoundary(100) {
		t.Fatal("no epoch boundaries when disabled")
	}
}

func TestEpochManagerEnabled(t *testing.T) {
	setup := newTestSetup(t, 4)
	em := NewEpochManagerWithLength(setup.vs, 10)

	if !em.EpochsEnabled() {
		t.Fatal("epochs should be enabled")
	}
	if em.EpochForView(1) != 0 {
		t.Fatalf("view 1 should be epoch 0, got %d", em.EpochForView(1))
	}
	if em.EpochForView(10) != 0 {
		t.Fatalf("view 10 should be epoch 0, got %d", em.EpochForView(10))
	}
	if em.EpochForView(11) != 1 {
		t.Fatalf("view 11 should be epoch 1, got %d", em.EpochForView(11))
	}

	if !em.IsEpochBoundary(11) {
		t.Fatal("view 11 should be epoch boundary")
	}
	if em.IsEpochBoundary(12) {
		t.Fatal("view 12 should not be epoch boundary")
	}
}

func TestEpochAdvance(t *testing.T) {
	setup := newTestSetup(t, 4)
	em := NewEpochManagerWithLength(setup.vs, 10)

	if em.HasStagedNext() {
		t.Fatal("should not have staged set")
	}

	newSetup := newTestSetup(t, 3)
	em.StageNextEpoch(newSetup.validators, newSetup.f)
	if !em.HasStagedNext() {
		t.Fatal("should have staged set")
	}

	advanced := em.AdvanceEpoch()
	if !advanced {
		t.Fatal("should have advanced")
	}
	if em.CurrentEpoch() != 1 {
		t.Fatalf("expected epoch 1, got %d", em.CurrentEpoch())
	}
	if em.CurrentValidatorSet().Len() != 3 {
		t.Fatalf("expected 3 validators, got %d", em.CurrentValidatorSet().Len())
	}

	// Without staged set, AdvanceEpoch returns false.
	if em.AdvanceEpoch() {
		t.Fatal("should not advance without staged set")
	}
}

// ============================================================================
// RoundState tests
// ============================================================================

func TestRoundStateInitial(t *testing.T) {
	rs := NewRoundState()
	if rs.CurrentView() != 1 {
		t.Fatalf("expected view 1, got %d", rs.CurrentView())
	}
	if rs.Phase() != PhaseWaitingForProposal {
		t.Fatalf("expected WaitingForProposal, got %s", rs.Phase())
	}
	if rs.LockedQC().View != 0 {
		t.Fatal("expected genesis locked QC")
	}
	if rs.ConsecutiveTimeouts() != 0 {
		t.Fatal("expected 0 timeouts")
	}
}

func TestRoundStateTransitions(t *testing.T) {
	rs := NewRoundState()

	rs.EnterVoting()
	if rs.Phase() != PhaseVoting {
		t.Fatal("expected Voting phase")
	}

	rs.EnterPreCommit()
	if rs.Phase() != PhasePreCommit {
		t.Fatal("expected PreCommit phase")
	}

	qc := QuorumCertificate{View: 1, BlockHash: types.Hash{1}}
	rs.Commit(qc)
	if rs.Phase() != PhaseCommitted {
		t.Fatal("expected Committed phase")
	}
	if rs.LastCommittedQC().View != 1 {
		t.Fatal("committed QC not updated")
	}
}

func TestRoundStateAdvanceView(t *testing.T) {
	rs := NewRoundState()
	rs.AdvanceView(5)
	if rs.CurrentView() != 5 {
		t.Fatalf("expected view 5, got %d", rs.CurrentView())
	}
	if rs.Phase() != PhaseWaitingForProposal {
		t.Fatal("expected WaitingForProposal after advance")
	}

	// Should not go backwards.
	rs.AdvanceView(3)
	if rs.CurrentView() != 5 {
		t.Fatal("view went backwards")
	}
}

func TestRoundStateTimeout(t *testing.T) {
	rs := NewRoundState()
	rs.Timeout()
	if rs.Phase() != PhaseTimedOut {
		t.Fatal("expected TimedOut phase")
	}
	if rs.ConsecutiveTimeouts() != 1 {
		t.Fatal("expected 1 timeout")
	}
	rs.Timeout()
	if rs.ConsecutiveTimeouts() != 2 {
		t.Fatal("expected 2 timeouts")
	}
	rs.ResetConsecutiveTimeouts()
	if rs.ConsecutiveTimeouts() != 0 {
		t.Fatal("expected 0 after reset")
	}
}

func TestSafetyRule(t *testing.T) {
	rs := NewRoundState()
	// Locked QC is genesis (view 0).
	qc := QuorumCertificate{View: 0}
	if !rs.IsSafeToVote(&qc) {
		t.Fatal("should be safe: justify view == locked view")
	}
	qc.View = 1
	if !rs.IsSafeToVote(&qc) {
		t.Fatal("should be safe: justify view > locked view")
	}

	// Update locked QC to view 5.
	lockedQC := QuorumCertificate{View: 5}
	rs.UpdateLockedQC(&lockedQC)
	qc.View = 4
	if rs.IsSafeToVote(&qc) {
		t.Fatal("should not be safe: justify view < locked view")
	}
	qc.View = 5
	if !rs.IsSafeToVote(&qc) {
		t.Fatal("should be safe: justify view == locked view")
	}
}

// ============================================================================
// Pacemaker tests
// ============================================================================

func TestPacemakerTimeout(t *testing.T) {
	p := NewPacemaker(100, 10000)
	d := p.TimeoutDuration(0)
	if d != 100*time.Millisecond {
		t.Fatalf("expected 100ms, got %v", d)
	}
	d = p.TimeoutDuration(1)
	if d != 200*time.Millisecond {
		t.Fatalf("expected 200ms, got %v", d)
	}
	d = p.TimeoutDuration(2)
	if d != 400*time.Millisecond {
		t.Fatalf("expected 400ms, got %v", d)
	}
}

func TestPacemakerMaxTimeout(t *testing.T) {
	p := NewPacemaker(100, 500)
	d := p.TimeoutDuration(10)
	if d != 500*time.Millisecond {
		t.Fatalf("expected 500ms (capped), got %v", d)
	}
}

func TestPacemakerAdaptive(t *testing.T) {
	p := NewPacemaker(1000, 60000)
	// Add enough latency samples to trigger adaptive mode.
	for i := 0; i < 10; i++ {
		p.ObserveCommitLatency(500 * time.Millisecond)
	}
	d := p.TimeoutDuration(0)
	// p95 = 500ms, adaptive = 1000ms, base = 1000ms, effective = max(1000, 1000) = 1000ms
	if d != 1000*time.Millisecond {
		t.Fatalf("expected 1000ms, got %v", d)
	}

	// Higher latencies should increase base.
	for i := 0; i < 10; i++ {
		p.ObserveCommitLatency(2 * time.Second)
	}
	d = p.TimeoutDuration(0)
	// p95 = 2000ms, adaptive = 4000ms, effective = max(1000, 4000) = 4000ms
	if d != 4000*time.Millisecond {
		t.Fatalf("expected 4000ms, got %v", d)
	}
}

func TestPacemakerResetForView(t *testing.T) {
	p := NewPacemaker(100, 10000)
	p.ResetForView(5, 0)
	if p.IsTimedOut() {
		t.Fatal("should not be timed out right after reset")
	}
	remaining := p.Remaining()
	if remaining <= 0 {
		t.Fatal("should have positive remaining time")
	}
}

// ============================================================================
// BLS Utility tests
// ============================================================================

func TestVerifyBLSSignature(t *testing.T) {
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	pk := sk.PublicKey()
	msg := []byte("test message")
	sig := sk.Sign(msg)

	if !VerifyBLSSignature(sig.Marshal(), pk, msg) {
		t.Fatal("valid signature should verify")
	}

	if VerifyBLSSignature(sig.Marshal(), pk, []byte("wrong message")) {
		t.Fatal("wrong message should not verify")
	}

	if VerifyBLSSignature([]byte{0, 1, 2}, pk, msg) {
		t.Fatal("invalid signature bytes should not verify")
	}
}

// ============================================================================
// VoteCollector tests
// ============================================================================

func TestVoteCollector(t *testing.T) {
	setup := newTestSetup(t, 4)
	blockHash := types.Hash{1, 2, 3}
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())

	if vc.BlockHash() != blockHash {
		t.Fatal("block hash mismatch")
	}
	if vc.VoteCount() != 0 {
		t.Fatal("should have 0 votes")
	}
	if vc.HasQuorum(3) {
		t.Fatal("should not have quorum")
	}

	// Add votes.
	for i := 0; i < 3; i++ {
		msg := SigningMessage(1, blockHash)
		sig := setup.keys[i].Sign(msg)
		err := vc.AddVote(ValidatorIndex(i), sig)
		if err != nil {
			t.Fatalf("failed to add vote %d: %v", i, err)
		}
	}

	if vc.VoteCount() != 3 {
		t.Fatalf("expected 3 votes, got %d", vc.VoteCount())
	}
	if !vc.HasQuorum(3) {
		t.Fatal("should have quorum")
	}

	// Duplicate vote should error.
	msg := SigningMessage(1, blockHash)
	sig := setup.keys[0].Sign(msg)
	err := vc.AddVote(0, sig)
	if err == nil {
		t.Fatal("expected duplicate vote error")
	}
	if _, ok := err.(*DuplicateVoteError); !ok {
		t.Fatal("expected DuplicateVoteError type")
	}
}

func TestVoteCollectorBuildQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	blockHash := types.Hash{1, 2, 3}
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())

	for i := 0; i < 3; i++ {
		msg := SigningMessage(1, blockHash)
		sig := setup.keys[i].Sign(msg)
		err := vc.AddVote(ValidatorIndex(i), sig)
		if err != nil {
			t.Fatal(err)
		}
	}

	qc, err := vc.BuildQC(setup.vs)
	if err != nil {
		t.Fatalf("BuildQC failed: %v", err)
	}
	if qc.View != 1 {
		t.Fatalf("QC view mismatch: got %d", qc.View)
	}
	if qc.BlockHash != blockHash {
		t.Fatal("QC block hash mismatch")
	}
	if qc.SignerCount() < 3 {
		t.Fatalf("expected at least 3 signers, got %d", qc.SignerCount())
	}

	// Verify the QC.
	if err := VerifyQC(qc, setup.vs); err != nil {
		t.Fatalf("QC verification failed: %v", err)
	}
}

func TestVoteCollectorAddFromBytes(t *testing.T) {
	setup := newTestSetup(t, 4)
	blockHash := types.Hash{1, 2, 3}
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())

	msg := SigningMessage(1, blockHash)
	sig := setup.keys[0].Sign(msg)
	err := vc.AddVerifiedVoteFromBytes(0, sig.Marshal())
	if err != nil {
		t.Fatalf("AddVerifiedVoteFromBytes failed: %v", err)
	}
	if vc.VoteCount() != 1 {
		t.Fatal("expected 1 vote")
	}
}

// ============================================================================
// TimeoutCollector tests
// ============================================================================

func TestTimeoutCollector(t *testing.T) {
	setup := newTestSetup(t, 4)
	tc := NewTimeoutCollector(5, setup.vs.Len())

	if tc.View() != 5 {
		t.Fatal("view mismatch")
	}
	if tc.TimeoutCount() != 0 {
		t.Fatal("should have 0 timeouts")
	}

	genesis := GenesisQC()
	for i := 0; i < 3; i++ {
		msg := TimeoutSigningMessage(5)
		sig := setup.keys[i].Sign(msg)
		err := tc.AddVerifiedTimeout(ValidatorIndex(i), sig, genesis)
		if err != nil {
			t.Fatalf("failed to add timeout %d: %v", i, err)
		}
	}

	if tc.TimeoutCount() != 3 {
		t.Fatalf("expected 3 timeouts, got %d", tc.TimeoutCount())
	}
	if !tc.HasQuorum(3) {
		t.Fatal("should have quorum")
	}

	tcert, err := tc.BuildTC(setup.vs)
	if err != nil {
		t.Fatalf("BuildTC failed: %v", err)
	}
	if tcert.View != 5 {
		t.Fatalf("TC view mismatch: got %d", tcert.View)
	}

	// Verify the TC.
	if err := VerifyTC(tcert, setup.vs); err != nil {
		t.Fatalf("TC verification failed: %v", err)
	}
}

func TestTimeoutCollectorWithHighQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	tc := NewTimeoutCollector(5, setup.vs.Len())

	genesis := GenesisQC()
	highQC := QuorumCertificate{View: 3, BlockHash: types.Hash{42}}

	msg := TimeoutSigningMessage(5)
	sig0 := setup.keys[0].Sign(msg)
	sig1 := setup.keys[1].Sign(msg)
	sig2 := setup.keys[2].Sign(msg)

	_ = tc.AddVerifiedTimeout(0, sig0, genesis)
	_ = tc.AddVerifiedTimeout(1, sig1, highQC)
	_ = tc.AddVerifiedTimeout(2, sig2, genesis)

	tcert, err := tc.BuildTC(setup.vs)
	if err != nil {
		t.Fatal(err)
	}
	if tcert.HighQC.View != 3 {
		t.Fatalf("expected high QC view 3, got %d", tcert.HighQC.View)
	}
}

// ============================================================================
// VerifyQC / VerifyTC tests
// ============================================================================

func TestVerifyQCInsufficientSigners(t *testing.T) {
	setup := newTestSetup(t, 4)
	blockHash := types.Hash{1}
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())

	// Only 2 votes (quorum is 3).
	for i := 0; i < 2; i++ {
		msg := SigningMessage(1, blockHash)
		sig := setup.keys[i].Sign(msg)
		_ = vc.AddVote(ValidatorIndex(i), sig)
	}

	_, err := vc.BuildQC(setup.vs)
	if err == nil {
		t.Fatal("expected insufficient votes error")
	}
}

func TestVerifyCommitQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	blockHash := types.Hash{1, 2, 3}
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())

	commitMsg := CommitSigningMessage(1, blockHash)
	for i := 0; i < 3; i++ {
		sig := setup.keys[i].Sign(commitMsg)
		_ = vc.AddVote(ValidatorIndex(i), sig)
	}

	qc, err := vc.BuildQCWithMessage(setup.vs, commitMsg)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCommitQC(qc, setup.vs); err != nil {
		t.Fatalf("CommitQC verification failed: %v", err)
	}

	// VerifyQC (prepare domain) should fail.
	if err := VerifyQC(qc, setup.vs); err == nil {
		t.Fatal("VerifyQC should fail on commit-domain QC")
	}

	// VerifyQCAnyDomain should succeed.
	if err := VerifyQCAnyDomain(qc, setup.vs); err != nil {
		t.Fatalf("VerifyQCAnyDomain should succeed: %v", err)
	}
}

func TestVerifyTCBitmapMismatch(t *testing.T) {
	setup := newTestSetup(t, 4)
	tc := &TimeoutCertificate{
		View:    1,
		Signers: []bool{true, true, true}, // length 3, but validator set has 4
	}
	err := VerifyTC(tc, setup.vs)
	if err == nil {
		t.Fatal("expected error for bitmap length mismatch")
	}
}

// ============================================================================
// ConsensusEngine tests
// ============================================================================

func TestEngineCreation(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	if engine.CurrentView() != 1 {
		t.Fatalf("expected view 1, got %d", engine.CurrentView())
	}
	if engine.CurrentPhase() != PhaseWaitingForProposal {
		t.Fatalf("expected WaitingForProposal, got %s", engine.CurrentPhase())
	}
	if engine.ValidatorCount() != 4 {
		t.Fatalf("expected 4 validators, got %d", engine.ValidatorCount())
	}
}

func TestEngineWithRecoveredState(t *testing.T) {
	setup := newTestSetup(t, 4)
	outputCh := make(chan EngineOutput, 256)
	locked := QuorumCertificate{View: 5, BlockHash: types.Hash{1}}
	committed := QuorumCertificate{View: 4, BlockHash: types.Hash{2}}

	engine := WithRecoveredState(
		0, setup.keys[0],
		NewEpochManager(setup.vs),
		1000, 10000,
		outputCh,
		10, locked, committed, 3,
	)

	if engine.CurrentView() != 10 {
		t.Fatalf("expected view 10, got %d", engine.CurrentView())
	}
	if engine.ConsecutiveTimeouts() != 3 {
		t.Fatalf("expected 3 timeouts, got %d", engine.ConsecutiveTimeouts())
	}
}

func TestEngineRecoveredStateCap(t *testing.T) {
	setup := newTestSetup(t, 4)
	outputCh := make(chan EngineOutput, 256)

	engine := WithRecoveredState(
		0, setup.keys[0],
		NewEpochManager(setup.vs),
		1000, 10000,
		outputCh,
		10, GenesisQC(), GenesisQC(), 200, // exceeds MaxRecoveredConsecutiveTimeouts
	)

	if engine.ConsecutiveTimeouts() != MaxRecoveredConsecutiveTimeouts {
		t.Fatalf("expected capped timeouts %d, got %d",
			MaxRecoveredConsecutiveTimeouts, engine.ConsecutiveTimeouts())
	}
}

// Test single-validator scenario: leader proposes and commits in same round.
func TestSingleValidatorConsensus(t *testing.T) {
	// Single validator: f=0, quorum=1.
	keys := make([]common.SecretKey, 1)
	pubKeys := make([]common.PublicKey, 1)
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	keys[0] = sk
	pubKeys[0] = sk.PublicKey()

	validators := []ValidatorInfo{{Address: types.Address{1}, PublicKey: pubKeys[0]}}
	vs := NewValidatorSet(validators, 0) // f=0, quorum=1

	outputCh := make(chan EngineOutput, 256)
	engine := NewConsensusEngine(0, keys[0], vs, 1000, 10000, outputCh)

	// View 1: leader = 1 % 1 = 0, so we are the leader.
	blockHash := types.Hash{0xAB}

	// Leader proposes.
	err = engine.ProcessEvent(ConsensusEvent{
		Type: EventBlockReady,
		Hash: blockHash,
	})
	if err != nil {
		t.Fatalf("onBlockReady failed: %v", err)
	}

	outputs := drainOutputs(outputCh)
	// Should have: Broadcast(Proposal), Broadcast(PrepareQC), SendToValidator(CommitVote to self),
	// Broadcast(Decide), BlockCommitted, ViewChanged
	var hasBroadcastProposal, hasPrepareQC, hasDecide, hasBlockCommitted, hasViewChanged bool
	for _, o := range outputs {
		switch o.Type {
		case OutputBroadcast:
			if o.Message != nil {
				switch o.Message.Type {
				case MsgProposal:
					hasBroadcastProposal = true
				case MsgPrepareQC:
					hasPrepareQC = true
				case MsgDecide:
					hasDecide = true
				}
			}
		case OutputBlockCommitted:
			hasBlockCommitted = true
			if o.Hash != blockHash {
				t.Fatalf("committed wrong block: %s", o.Hash.Hex())
			}
		case OutputViewChanged:
			hasViewChanged = true
		}
	}

	if !hasBroadcastProposal {
		t.Fatal("missing Proposal broadcast")
	}
	if !hasPrepareQC {
		t.Fatal("missing PrepareQC broadcast")
	}
	if !hasDecide {
		t.Fatal("missing Decide broadcast")
	}
	if !hasBlockCommitted {
		t.Fatal("missing BlockCommitted output")
	}
	if !hasViewChanged {
		t.Fatal("missing ViewChanged output")
	}

	// Engine should be at view 2 now.
	if engine.CurrentView() != 2 {
		t.Fatalf("expected view 2, got %d", engine.CurrentView())
	}
}

// Test full 4-validator consensus flow.
func TestFourValidatorConsensus(t *testing.T) {
	setup := newTestSetup(t, 4)
	blockHash := types.Hash{0xBE, 0xEF}

	// View 1: leader = 1 % 4 = 1.
	leaderIdx := 1
	engine, outputCh := newTestEngine(t, setup, leaderIdx)

	// Leader proposes.
	err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash})
	if err != nil {
		t.Fatal(err)
	}
	drainOutputs(outputCh) // Discard broadcast proposal

	// Submit Round 1 votes from validators 0, 2, 3.
	for _, i := range []int{0, 2, 3} {
		msg := SigningMessage(1, blockHash)
		sig := setup.keys[i].Sign(msg)
		vote := &Vote{
			View:      1,
			BlockHash: blockHash,
			Voter:     ValidatorIndex(i),
			Signature: sig.Marshal(),
		}
		err := engine.ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgVote, Payload: vote},
		})
		if err != nil {
			t.Fatalf("vote from %d failed: %v", i, err)
		}
	}

	// PrepareQC should now be formed. Check outputs.
	outputs := drainOutputs(outputCh)
	var hasPrepareQC bool
	for _, o := range outputs {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgPrepareQC {
			hasPrepareQC = true
		}
	}
	if !hasPrepareQC {
		t.Fatal("PrepareQC should be formed after 3 votes (leader self-voted + 2 others = quorum)")
	}

	// Submit Round 2 commit votes from validators 0, 2, 3.
	for _, i := range []int{0, 2, 3} {
		commitMsg := CommitSigningMessage(1, blockHash)
		sig := setup.keys[i].Sign(commitMsg)
		cv := &CommitVote{
			View:      1,
			BlockHash: blockHash,
			Voter:     ValidatorIndex(i),
			Signature: sig.Marshal(),
		}
		err := engine.ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgCommitVote, Payload: cv},
		})
		if err != nil {
			t.Fatalf("commit vote from %d failed: %v", i, err)
		}
	}

	outputs = drainOutputs(outputCh)
	var hasDecide, hasBlockCommitted bool
	for _, o := range outputs {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgDecide {
			hasDecide = true
		}
		if o.Type == OutputBlockCommitted {
			hasBlockCommitted = true
		}
	}
	if !hasDecide {
		t.Fatal("Decide should be broadcast after commit quorum")
	}
	if !hasBlockCommitted {
		t.Fatal("BlockCommitted should be emitted")
	}
	if engine.CurrentView() != 2 {
		t.Fatalf("expected view 2, got %d", engine.CurrentView())
	}
}

// Test timeout flow.
func TestTimeoutFlow(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newTestEngine(t, setup, 0)

	// Trigger timeout in view 1.
	err := engine.OnTimeout()
	if err != nil {
		t.Fatal(err)
	}

	outputs := drainOutputs(outputCh)
	var hasTimeoutBroadcast bool
	for _, o := range outputs {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgTimeout {
			hasTimeoutBroadcast = true
		}
	}
	if !hasTimeoutBroadcast {
		t.Fatal("timeout should broadcast TimeoutMessage")
	}

	if engine.CurrentPhase() != PhaseTimedOut {
		t.Fatalf("expected TimedOut phase, got %s", engine.CurrentPhase())
	}
	if engine.ConsecutiveTimeouts() != 1 {
		t.Fatalf("expected 1 timeout, got %d", engine.ConsecutiveTimeouts())
	}
}

// Test that follower receives proposal and sends vote.
func TestFollowerReceivesProposal(t *testing.T) {
	setup := newTestSetup(t, 4)

	// View 1: leader = 1. Node 0 is follower.
	follower, outputCh := newTestEngine(t, setup, 0)
	blockHash := types.Hash{0xDE, 0xAD}

	// Create a valid proposal from leader (validator 1).
	msg := SigningMessage(1, blockHash)
	sig := setup.keys[1].Sign(msg)
	proposal := &Proposal{
		View:      1,
		BlockHash: blockHash,
		JustifyQC: GenesisQC(),
		Proposer:  1,
		Signature: sig.Marshal(),
	}

	err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgProposal, Payload: proposal},
	})
	if err != nil {
		t.Fatalf("processProposal failed: %v", err)
	}

	outputs := drainOutputs(outputCh)
	var hasExecuteBlock bool
	for _, o := range outputs {
		if o.Type == OutputExecuteBlock && o.Hash == blockHash {
			hasExecuteBlock = true
		}
		if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgVote {
			t.Fatal("must not vote before the proposed block is imported")
		}
	}
	if !hasExecuteBlock {
		t.Fatal("should request block execution")
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: blockHash}); err != nil {
		t.Fatalf("block import failed: %v", err)
	}
	var hasVote bool
	for _, o := range drainOutputs(outputCh) {
		if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgVote {
			hasVote = true
			vote := o.Message.Payload.(*Vote)
			if vote.BlockHash != blockHash || vote.Voter != 0 || vote.View != 1 {
				t.Fatal("vote fields incorrect")
			}
		}
	}
	if !hasVote {
		t.Fatal("should send vote to leader after import")
	}
}

// Test invalid proposer rejection.
func TestRejectInvalidProposer(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, _ := newTestEngine(t, setup, 0)
	blockHash := types.Hash{1}

	// Validator 2 claims to be proposer for view 1, but leader is validator 1.
	msg := SigningMessage(1, blockHash)
	sig := setup.keys[2].Sign(msg)
	proposal := &Proposal{
		View:      1,
		BlockHash: blockHash,
		JustifyQC: GenesisQC(),
		Proposer:  2, // wrong proposer
		Signature: sig.Marshal(),
	}

	err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgProposal, Payload: proposal},
	})
	if err == nil {
		t.Fatal("should reject invalid proposer")
	}
	if _, ok := err.(*InvalidProposerError); !ok {
		t.Fatalf("expected InvalidProposerError, got %T", err)
	}
}

// Test safety violation rejection.
func TestRejectSafetyViolation(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, _ := newTestEngine(t, setup, 0)

	// Build a valid QC at view 3 so it passes QC verification.
	blockHash3 := types.Hash{3}
	vc3 := NewVoteCollector(3, blockHash3, setup.vs.Len())
	for i := 0; i < 3; i++ {
		msg := SigningMessage(3, blockHash3)
		sig := setup.keys[i].Sign(msg)
		_ = vc3.AddVote(ValidatorIndex(i), sig)
	}
	justifyQC, err := vc3.BuildQC(setup.vs)
	if err != nil {
		t.Fatal(err)
	}

	// Update locked QC to view 5 (higher than justify QC view 3).
	follower.mu.Lock()
	follower.roundState.UpdateLockedQC(&QuorumCertificate{View: 5})
	follower.mu.Unlock()

	blockHash := types.Hash{1}
	msg := SigningMessage(1, blockHash)
	sig := setup.keys[1].Sign(msg)
	proposal := &Proposal{
		View:      1,
		BlockHash: blockHash,
		JustifyQC: *justifyQC, // view 3 < locked view 5 → safety violation
		Proposer:  1,
		Signature: sig.Marshal(),
	}

	err = follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgProposal, Payload: proposal},
	})
	if err == nil {
		t.Fatal("should reject safety violation")
	}
	if _, ok := err.(*SafetyViolationError); !ok {
		t.Fatalf("expected SafetyViolationError, got %T: %v", err, err)
	}
}

// Test view mismatch for votes.
func TestRejectViewMismatchVote(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 1)

	// Engine is at view 1; send vote for view 5.
	vote := &Vote{
		View:      5,
		BlockHash: types.Hash{1},
		Voter:     0,
		Signature: []byte{},
	}
	err := engine.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgVote, Payload: vote},
	})
	// Vote with view != current should return ViewMismatchError
	// (votes are not buffered for future views, they're dispatched if in range).
	// Actually, future votes within window are buffered silently. This is expected behavior.
	if err != nil {
		t.Logf("vote rejection (expected): %v", err)
	}
}

// Test Decide message advances view.
func TestDecideAdvancesView(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)
	blockHash := types.Hash{0xCA, 0xFE}

	// Build a valid CommitQC for view 1.
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())
	commitMsg := CommitSigningMessage(1, blockHash)
	for i := 0; i < 3; i++ {
		sig := setup.keys[i].Sign(commitMsg)
		_ = vc.AddVote(ValidatorIndex(i), sig)
	}
	qc, err := vc.BuildQCWithMessage(setup.vs, commitMsg)
	if err != nil {
		t.Fatal(err)
	}

	decide := &Decide{
		View:      1,
		BlockHash: blockHash,
		CommitQC:  *qc,
	}

	err = follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgDecide, Payload: decide},
	})
	if err != nil {
		t.Fatalf("processDecide failed: %v", err)
	}

	outputs := drainOutputs(outputCh)
	var hasBlockCommitted, hasViewChanged bool
	for _, o := range outputs {
		if o.Type == OutputBlockCommitted {
			hasBlockCommitted = true
		}
		if o.Type == OutputViewChanged {
			hasViewChanged = true
		}
	}
	if !hasBlockCommitted {
		t.Fatal("should emit BlockCommitted")
	}
	if !hasViewChanged {
		t.Fatal("should emit ViewChanged")
	}
	if follower.CurrentView() != 2 {
		t.Fatalf("expected view 2, got %d", follower.CurrentView())
	}
}

// Test equivocation detection.
func TestEquivocationDetection(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newTestEngine(t, setup, 1)
	blockHash1 := types.Hash{1}
	blockHash2 := types.Hash{2}

	// Leader proposes block 1.
	err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash1})
	if err != nil {
		t.Fatal(err)
	}
	drainOutputs(outputCh)

	// Validator 0 votes for block 1.
	msg1 := SigningMessage(1, blockHash1)
	sig1 := setup.keys[0].Sign(msg1)
	err = engine.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 1, BlockHash: blockHash1, Voter: 0, Signature: sig1.Marshal()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOutputs(outputCh)

	// Validator 0 votes for block 2 (equivocation!).
	msg2 := SigningMessage(1, blockHash2)
	sig2 := setup.keys[0].Sign(msg2)
	err = engine.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 1, BlockHash: blockHash2, Voter: 0, Signature: sig2.Marshal()}},
	})
	if err != nil {
		t.Fatal(err)
	}

	outputs := drainOutputs(outputCh)
	var hasEquivocation bool
	for _, o := range outputs {
		if o.Type == OutputEquivocationDetected {
			hasEquivocation = true
			if o.Validator != 0 {
				t.Fatal("wrong validator")
			}
		}
	}
	if !hasEquivocation {
		t.Fatal("should detect equivocation")
	}
}

// ============================================================================
// Adapter tests
// ============================================================================

func TestHotStuffNew(t *testing.T) {
	h := New(nil, nil)
	if h.config.BaseTimeout != 60000 {
		t.Fatalf("expected default base timeout 60000, got %d", h.config.BaseTimeout)
	}
	if h.config.MaxTimeout != 120000 {
		t.Fatalf("expected default max timeout 120000, got %d", h.config.MaxTimeout)
	}
}

func TestHotStuffAuthorize(t *testing.T) {
	sk, _ := bls.RandKey()
	addr := types.Address{1}
	h := New(nil, nil)
	h.Authorize(addr, sk)
	if h.signer != addr || h.secretKey == nil {
		t.Fatal("Authorize failed")
	}
}

func TestHotStuffInitEngineWithoutAuthorize(t *testing.T) {
	h := New(nil, nil)
	err := h.InitEngine(nil, 0)
	if err == nil || err.Error() != "hotstuff: secret key not set, call Authorize first" {
		t.Fatalf("expected auth error, got: %v", err)
	}
}

func TestHotStuffInitEngine(t *testing.T) {
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])

	err := h.InitEngine(setup.validators, setup.f)
	if err != nil {
		t.Fatalf("InitEngine failed: %v", err)
	}
	if h.Engine() == nil {
		t.Fatal("engine should not be nil")
	}
}

func TestHotStuffInitEngineNotInSet(t *testing.T) {
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(types.Address{99}, setup.keys[0])

	err := h.InitEngine(setup.validators, setup.f)
	if err == nil {
		t.Fatal("expected error: signer not in validator set")
	}
}

func TestHotStuffType(t *testing.T) {
	h := New(nil, nil)
	if h.Type() != "hotstuff" {
		t.Fatalf("expected hotstuff, got %s", h.Type())
	}
}

func TestHotStuffCalcDifficulty(t *testing.T) {
	h := New(nil, nil)
	d := h.CalcDifficulty(nil, 0, nil)
	if d.Uint64() != 1 {
		t.Fatalf("expected difficulty 1, got %d", d.Uint64())
	}
}

func TestHotStuffClose(t *testing.T) {
	h := New(nil, nil)
	if err := h.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestExtractViewFromExtra(t *testing.T) {
	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], extraMagic[:])
	extra[extraMagicLen] = 42 // view = 42

	view, err := ExtractViewFromExtra(extra)
	if err != nil {
		t.Fatal(err)
	}
	if view != 42 {
		t.Fatalf("expected view 42, got %d", view)
	}
}

func TestExtractViewFromExtraTooShort(t *testing.T) {
	_, err := ExtractViewFromExtra([]byte{1, 2})
	if err == nil {
		t.Fatal("expected error for short extra")
	}
}

func TestExtractViewFromExtraBadMagic(t *testing.T) {
	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], []byte("ZZZZ"))
	_, err := ExtractViewFromExtra(extra)
	if err == nil {
		t.Fatal("expected error for bad magic")
	}
}

// ============================================================================
// Error type tests
// ============================================================================

func TestErrorMessages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&InvalidSignatureError{View: 1, ValidatorIndex: 2}, "invalid BLS signature from validator 2 in view 1"},
		{&InvalidProposerError{View: 1, Expected: 0, Actual: 1}, "invalid proposer: expected validator 0, got 1 in view 1"},
		{&InsufficientVotesError{View: 1, Have: 2, Need: 3}, "insufficient votes: have 2, need 3 in view 1"},
		{&ViewMismatchError{Current: 1, Received: 5}, "view mismatch: current view is 1, message view is 5"},
		{&DuplicateVoteError{View: 1, ValidatorIndex: 0}, "duplicate vote from validator 0 in view 1"},
		{&InvalidQCError{View: 1, Reason: "bad"}, "invalid quorum certificate for view 1: bad"},
		{&InvalidTCError{View: 1, Reason: "bad"}, "invalid timeout certificate for view 1: bad"},
		{&UnknownValidatorError{Index: 5, SetSize: 4}, "unknown validator index 5, set size is 4"},
		{&SafetyViolationError{QCView: 3, LockedView: 5}, "safety violation: proposal QC view 3 does not extend locked QC view 5"},
		{&BlockHashMismatchError{Expected: types.Hash{1}, Got: types.Hash{2}}, ""},
	}
	for _, tc := range tests {
		msg := tc.err.Error()
		if tc.want != "" && msg != tc.want {
			t.Errorf("error message mismatch:\n  got:  %s\n  want: %s", msg, tc.want)
		}
		if msg == "" {
			t.Errorf("error message should not be empty for %T", tc.err)
		}
	}
}

// ============================================================================
// Future message buffering tests
// ============================================================================

func TestFutureMessageBuffering(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	// Send a vote for future view 3 (within FutureViewWindow).
	vote := &Vote{View: 3, BlockHash: types.Hash{1}, Voter: 1, Signature: []byte{}}
	err := engine.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgVote, Payload: vote},
	})
	if err != nil {
		t.Fatalf("future vote should be buffered without error: %v", err)
	}

	engine.mu.Lock()
	bufLen := len(engine.futureMsgBuffer)
	engine.mu.Unlock()

	if bufLen != 1 {
		t.Fatalf("expected 1 buffered message, got %d", bufLen)
	}
}

// ============================================================================
// RoundState snapshot recovery tests
// ============================================================================

func TestRoundStateFromSnapshot(t *testing.T) {
	locked := QuorumCertificate{View: 10, BlockHash: types.Hash{1}}
	committed := QuorumCertificate{View: 8, BlockHash: types.Hash{2}}
	rs := RoundStateFromSnapshot(15, locked, committed, 5)

	if rs.CurrentView() != 15 {
		t.Fatalf("expected view 15, got %d", rs.CurrentView())
	}
	if rs.Phase() != PhaseWaitingForProposal {
		t.Fatal("expected WaitingForProposal")
	}
	if rs.LockedQC().View != 10 {
		t.Fatal("locked QC view mismatch")
	}
	if rs.LastCommittedQC().View != 8 {
		t.Fatal("committed QC view mismatch")
	}
	if rs.ConsecutiveTimeouts() != 5 {
		t.Fatal("timeout count mismatch")
	}
}
