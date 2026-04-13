// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// ----------------------------------------------------------------------------
// SR2 — Lock monotonicity. UpdateLockedQC must never decrement.
// ----------------------------------------------------------------------------

func TestByzantine_LockMonotonic(t *testing.T) {
	rs := NewRoundState()

	// Genesis lock starts at view 0.
	if rs.LockedQC().View != 0 {
		t.Fatalf("genesis lock view = %d, want 0", rs.LockedQC().View)
	}

	// Bump to view 5.
	rs.UpdateLockedQC(&QuorumCertificate{View: 5})
	if rs.LockedQC().View != 5 {
		t.Fatalf("after update to 5, lock view = %d", rs.LockedQC().View)
	}

	// Replay an older PrepareQC at view 3 — must NOT decrement.
	rs.UpdateLockedQC(&QuorumCertificate{View: 3})
	if rs.LockedQC().View != 5 {
		t.Fatalf("SR2 violated: lock decremented to %d after replay of view 3", rs.LockedQC().View)
	}

	// Equal view also leaves lock unchanged (only strictly greater wins).
	rs.UpdateLockedQC(&QuorumCertificate{View: 5})
	if rs.LockedQC().View != 5 {
		t.Fatalf("equal-view update should be a no-op, lock view = %d", rs.LockedQC().View)
	}

	// Strictly greater wins.
	rs.UpdateLockedQC(&QuorumCertificate{View: 7})
	if rs.LockedQC().View != 7 {
		t.Fatalf("after update to 7, lock view = %d", rs.LockedQC().View)
	}
}

// ----------------------------------------------------------------------------
// SR9 (Round 1) — A validator sends at most one prepare vote per (view, round).
//
// Drives the engine through processProposal twice with the same proposal.
// The first call should emit one OutputSendToValidator (the vote); the second
// must be suppressed by RoundState.HasVotedInView.
// ----------------------------------------------------------------------------

func TestByzantine_DoubleVoteSuppressed(t *testing.T) {
	setup := newTestSetup(t, 4)
	// Engine 0 is a follower at view 1; leader is view % n = 1.
	follower, outputCh := newTestEngine(t, setup, 0)

	// Construct a valid proposal from leader (index 1) for view 1.
	blockHash := types.Hash{0xAA}
	msg := SigningMessage(1, blockHash)
	leaderSig := setup.keys[1].Sign(msg)
	proposal := &Proposal{
		View:      1,
		BlockHash: blockHash,
		JustifyQC: GenesisQC(), // genesis is exempt from QC verify
		Proposer:  1,
		Signature: leaderSig.Marshal(),
	}

	// First delivery — follower should vote.
	if err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgProposal, Payload: proposal},
	}); err != nil {
		t.Fatalf("first proposal: %v", err)
	}
	out1 := drainOutputs(outputCh)
	votes1 := countOutputs(out1, OutputSendToValidator, MsgVote)
	if votes1 != 1 {
		t.Fatalf("first proposal: expected 1 prepare vote, got %d", votes1)
	}

	// Second delivery (replay) — vote must be suppressed.
	if err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgProposal, Payload: proposal},
	}); err != nil {
		t.Fatalf("second proposal: %v", err)
	}
	out2 := drainOutputs(outputCh)
	votes2 := countOutputs(out2, OutputSendToValidator, MsgVote)
	if votes2 != 0 {
		t.Fatalf("SR9 violated: replay produced %d additional prepare vote(s)", votes2)
	}
}

// ----------------------------------------------------------------------------
// SR9 (Round 2) — A validator sends at most one commit vote per (view, round).
//
// Sends two PrepareQCMsgs to a follower for the same view; only the first
// should produce an OutputSendToValidator(MsgCommitVote).
// ----------------------------------------------------------------------------

func TestByzantine_DoubleCommitVoteSuppressed(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)

	blockHash := types.Hash{0xBB}

	// Build a valid PrepareQC for view 1 with 3 signers (= quorum for n=4).
	prepareMsg := SigningMessage(1, blockHash)
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())
	for i := 0; i < 3; i++ {
		_ = vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(prepareMsg))
	}
	prepareQC, err := vc.BuildQC(setup.vs)
	if err != nil {
		t.Fatal(err)
	}

	pqcMsg := &PrepareQCMsg{View: 1, BlockHash: blockHash, QC: *prepareQC}

	// First delivery — follower commits-votes.
	if err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgPrepareQC, Payload: pqcMsg},
	}); err != nil {
		t.Fatalf("first PrepareQC: %v", err)
	}
	out1 := drainOutputs(outputCh)
	commit1 := countOutputs(out1, OutputSendToValidator, MsgCommitVote)
	if commit1 != 1 {
		t.Fatalf("first PrepareQC: expected 1 commit vote, got %d", commit1)
	}

	// Second delivery — must be suppressed.
	if err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgPrepareQC, Payload: pqcMsg},
	}); err != nil {
		t.Fatalf("second PrepareQC: %v", err)
	}
	out2 := drainOutputs(outputCh)
	commit2 := countOutputs(out2, OutputSendToValidator, MsgCommitVote)
	if commit2 != 0 {
		t.Fatalf("SR9 violated: replay produced %d additional commit vote(s)", commit2)
	}
}

// ----------------------------------------------------------------------------
// SR8 (Round 2) — Equivocation in commit votes is detected and surfaced as
// OutputEquivocationDetected. (Round 1 equivocation is already covered by
// hotstuff_test.go TestEquivocationDetection.)
// ----------------------------------------------------------------------------

func TestByzantine_CommitEquivocationDetected(t *testing.T) {
	setup := newTestSetup(t, 4)
	// Engine 1 is the leader for view 1 (1 % 4 == 1).
	leader, outputCh := newTestEngine(t, setup, 1)

	hashA := types.Hash{0xA}
	hashB := types.Hash{0xB}

	// Leader proposes a block so that commitCollector is bound to hashA.
	if err := leader.ProcessEvent(ConsensusEvent{
		Type: EventBlockReady,
		Hash: hashA,
	}); err != nil {
		t.Fatalf("onBlockReady: %v", err)
	}
	drainOutputs(outputCh)

	// Validator 0 sends a commit vote for hashA. Use the correct domain.
	commitMsgA := CommitSigningMessage(1, hashA)
	sigA := setup.keys[0].Sign(commitMsgA)
	if err := leader.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgCommitVote, Payload: &CommitVote{
			View: 1, BlockHash: hashA, Voter: 0, Signature: sigA.Marshal(),
		}},
	}); err != nil {
		t.Fatalf("first commit vote: %v", err)
	}
	drainOutputs(outputCh)

	// Validator 0 then sends a commit vote for hashB (equivocation).
	commitMsgB := CommitSigningMessage(1, hashB)
	sigB := setup.keys[0].Sign(commitMsgB)
	if err := leader.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg: ConsensusMsg{Type: MsgCommitVote, Payload: &CommitVote{
			View: 1, BlockHash: hashB, Voter: 0, Signature: sigB.Marshal(),
		}},
	}); err != nil {
		t.Fatalf("second commit vote: %v", err)
	}
	out := drainOutputs(outputCh)

	var found bool
	for _, o := range out {
		if o.Type == OutputEquivocationDetected && o.Validator == 0 {
			found = true
			if o.Hash1 != hashA || o.Hash2 != hashB {
				t.Fatalf("equivocation hashes mismatch: got (%x,%x)", o.Hash1, o.Hash2)
			}
		}
	}
	if !found {
		t.Fatal("SR8 violated: commit-vote equivocation was not surfaced")
	}
}

// ----------------------------------------------------------------------------
// SR6 — A QC with fewer than 2f+1 valid signers must be rejected.
//
// We construct a PrepareQC for view 3 with only 2 signers (n=4 → quorum=3),
// then verify it via VerifyQC. Verification must fail with InvalidQCError or
// InsufficientVotesError.
// ----------------------------------------------------------------------------

func TestByzantine_ForgedQCInsufficientSigners(t *testing.T) {
	setup := newTestSetup(t, 4)

	blockHash := types.Hash{0xCD}
	prepareMsg := SigningMessage(3, blockHash)

	vc := NewVoteCollector(3, blockHash, setup.vs.Len())
	// Only add 2 votes — one short of quorum (3).
	for i := 0; i < 2; i++ {
		_ = vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(prepareMsg))
	}

	// BuildQC must refuse to build at all.
	if _, err := vc.BuildQC(setup.vs); err == nil {
		t.Fatal("BuildQC accepted insufficient votes; SR6 violated at builder")
	}

	// As a defense in depth, also synthesize a hand-built QC with only 2
	// signers in the bitmap and feed it to VerifyQC. It must reject.
	signers := make([]bool, setup.vs.Len())
	signers[0] = true
	signers[1] = true
	forged := &QuorumCertificate{
		View:               3,
		BlockHash:          blockHash,
		AggregateSignature: []byte{}, // doesn't matter, signers count check fails first
		Signers:            signers,
	}
	if err := VerifyQC(forged, setup.vs); err == nil {
		t.Fatal("VerifyQC accepted forged QC with 2 signers; SR6 violated")
	}
}

// ----------------------------------------------------------------------------
// SR6 — Domain separation: a CommitQC must NOT pass as a PrepareQC.
//
// Build a valid CommitQC (Round 2 domain) and verify it via VerifyQC
// (Round 1 domain). Verification must fail. The cross-domain helper
// VerifyQCAnyDomain is the only API that should accept either form.
// ----------------------------------------------------------------------------

func TestByzantine_ForgedQCWrongDomain(t *testing.T) {
	setup := newTestSetup(t, 4)

	blockHash := types.Hash{0xEF}

	// Build a CommitQC: votes signed over CommitSigningMessage.
	commitMsg := CommitSigningMessage(2, blockHash)
	vc := NewVoteCollector(2, blockHash, setup.vs.Len())
	for i := 0; i < 3; i++ {
		_ = vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(commitMsg))
	}
	commitQC, err := vc.BuildQCWithMessage(setup.vs, commitMsg)
	if err != nil {
		t.Fatal(err)
	}

	// VerifyQC (PrepareQC domain) must reject — signers signed the wrong msg.
	if err := VerifyQC(commitQC, setup.vs); err == nil {
		t.Fatal("VerifyQC accepted a CommitQC; SR6 domain separation violated")
	}

	// VerifyCommitQC (CommitQC domain) must accept.
	if err := VerifyCommitQC(commitQC, setup.vs); err != nil {
		t.Fatalf("VerifyCommitQC rejected its own CommitQC: %v", err)
	}

	// VerifyQCAnyDomain must also accept.
	if err := VerifyQCAnyDomain(commitQC, setup.vs); err != nil {
		t.Fatalf("VerifyQCAnyDomain rejected a valid CommitQC: %v", err)
	}
}

// ----------------------------------------------------------------------------
// SR5 — Quorum boundary. n=4, f=1, quorum=3.
//   - Exactly 3 valid signers ⇒ BuildQC + VerifyQC succeed.
//   - 2 valid signers ⇒ BuildQC fails with InsufficientVotesError.
//
// Also exercises n=7, f=2, quorum=5 to confirm the formula scales.
// ----------------------------------------------------------------------------

func TestByzantine_QuorumBoundary(t *testing.T) {
	for _, tc := range []struct {
		name         string
		n            int
		expectQuorum int
	}{
		{"n=4 f=1", 4, 3},
		{"n=7 f=2", 7, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup := newTestSetup(t, tc.n)
			if got := setup.vs.QuorumSize(); got != tc.expectQuorum {
				t.Fatalf("QuorumSize() = %d, want %d", got, tc.expectQuorum)
			}

			blockHash := types.Hash{0x42}
			msg := SigningMessage(1, blockHash)

			// Below-quorum: tc.expectQuorum - 1 signers should fail.
			vcBelow := NewVoteCollector(1, blockHash, setup.vs.Len())
			for i := 0; i < tc.expectQuorum-1; i++ {
				_ = vcBelow.AddVote(ValidatorIndex(i), setup.keys[i].Sign(msg))
			}
			if _, err := vcBelow.BuildQC(setup.vs); err == nil {
				t.Fatalf("%d signers (one short of quorum) accepted; SR5 violated", tc.expectQuorum-1)
			}

			// Exactly quorum: tc.expectQuorum signers should succeed.
			vcAt := NewVoteCollector(1, blockHash, setup.vs.Len())
			for i := 0; i < tc.expectQuorum; i++ {
				_ = vcAt.AddVote(ValidatorIndex(i), setup.keys[i].Sign(msg))
			}
			qc, err := vcAt.BuildQC(setup.vs)
			if err != nil {
				t.Fatalf("exactly-quorum BuildQC failed: %v", err)
			}
			if err := VerifyQC(qc, setup.vs); err != nil {
				t.Fatalf("exactly-quorum VerifyQC failed: %v", err)
			}
			if qc.SignerCount() != tc.expectQuorum {
				t.Fatalf("SignerCount() = %d, want %d", qc.SignerCount(), tc.expectQuorum)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// SR4 — View change uses the highest high_qc seen in any TimeoutMessage.
//
// Construct a TimeoutCollector seeded with timeout messages whose embedded
// high_qc views are {2, 7, 4}. BuildTC must select the high_qc with view 7.
// ----------------------------------------------------------------------------

func TestByzantine_ViewChangeUsesHighestHighQC(t *testing.T) {
	setup := newTestSetup(t, 4)

	// Build three real high_qcs at views 2, 7, 4 so that BuildTC's
	// defensive re-verification (quorum.go:267) accepts the chosen one.
	makeQC := func(view ViewNumber) *QuorumCertificate {
		hash := types.Hash{byte(view)}
		msg := SigningMessage(view, hash)
		vc := NewVoteCollector(view, hash, setup.vs.Len())
		for i := 0; i < 3; i++ {
			_ = vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(msg))
		}
		qc, err := vc.BuildQC(setup.vs)
		if err != nil {
			t.Fatalf("makeQC(view=%d): %v", view, err)
		}
		return qc
	}
	highQCs := []QuorumCertificate{
		*makeQC(2),
		*makeQC(7),
		*makeQC(4),
	}

	// timeoutCollector at view 10. Three timeout messages from validators 0..2.
	tc := NewTimeoutCollector(10, setup.vs.Len())
	timeoutMsg := TimeoutSigningMessage(10)
	for i := 0; i < 3; i++ {
		sig := setup.keys[i].Sign(timeoutMsg)
		if err := tc.AddVerifiedTimeout(ValidatorIndex(i), sig, highQCs[i]); err != nil {
			t.Fatalf("AddVerifiedTimeout(%d): %v", i, err)
		}
	}

	built, err := tc.BuildTC(setup.vs)
	if err != nil {
		t.Fatalf("BuildTC: %v", err)
	}
	if built.HighQC.View != 7 {
		t.Fatalf("SR4 violated: TC selected high_qc.view=%d, want 7", built.HighQC.View)
	}
}

// ----------------------------------------------------------------------------
// SR3 + SR5 — Network partition.
//
// 7 nodes, partition into majority (5 nodes = 2f+1 with f=2) and minority
// (2 nodes). Run one consensus round routing messages only inside the
// majority partition. The majority must commit; the minority must NOT
// commit and must remain at the original view.
// ----------------------------------------------------------------------------

func TestByzantine_NetworkPartition(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping partition test in -short mode")
	}

	const n = 7
	h := newChaosHarness(t, n)

	// View 1 leader is index 1 (1 % 7). Place leader in the majority.
	majority := map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true}
	minority := map[int]bool{5: true, 6: true}
	view := ViewNumber(1)
	leaderIdx := int(view % uint64(n))
	if !majority[leaderIdx] {
		t.Fatalf("test setup wrong: leader %d not in majority", leaderIdx)
	}

	blockHash := blockHashForView(view)

	// Leader proposes.
	if err := h.engines[leaderIdx].ProcessEvent(ConsensusEvent{
		Type: EventBlockReady, Hash: blockHash,
	}); err != nil {
		t.Fatalf("leader propose: %v", err)
	}
	leaderOut := drainOutputs(h.channels[leaderIdx])
	var proposal *Proposal
	for _, o := range leaderOut {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgProposal {
			proposal = o.Message.Payload.(*Proposal)
		}
	}
	if proposal == nil {
		t.Fatal("no proposal broadcast")
	}

	// Route Proposal only inside majority.
	for i := 0; i < n; i++ {
		if i == leaderIdx || !majority[i] {
			continue
		}
		_ = h.engines[i].ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgProposal, Payload: proposal},
		})
	}

	// Collect prepare votes from majority followers, route to leader.
	for i := 0; i < n; i++ {
		if i == leaderIdx || !majority[i] {
			continue
		}
		for _, o := range drainOutputs(h.channels[i]) {
			if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgVote {
				_ = h.engines[leaderIdx].ProcessEvent(ConsensusEvent{
					Type: EventMessage,
					Msg:  *o.Message,
				})
			}
		}
	}

	// Drain leader, find PrepareQCMsg.
	leaderOut = drainOutputs(h.channels[leaderIdx])
	var pqcMsg *PrepareQCMsg
	for _, o := range leaderOut {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgPrepareQC {
			pqcMsg = o.Message.Payload.(*PrepareQCMsg)
		}
	}
	if pqcMsg == nil {
		t.Fatal("no PrepareQC broadcast — majority should have reached quorum (5 votes ≥ 5)")
	}

	// Route PrepareQC only inside majority.
	for i := 0; i < n; i++ {
		if i == leaderIdx || !majority[i] {
			continue
		}
		_ = h.engines[i].ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgPrepareQC, Payload: pqcMsg},
		})
	}

	// Collect commit votes, route to leader.
	for i := 0; i < n; i++ {
		if i == leaderIdx || !majority[i] {
			continue
		}
		for _, o := range drainOutputs(h.channels[i]) {
			if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgCommitVote {
				_ = h.engines[leaderIdx].ProcessEvent(ConsensusEvent{
					Type: EventMessage,
					Msg:  *o.Message,
				})
			}
		}
	}

	// Leader should have formed CommitQC and broadcast Decide.
	leaderOut = drainOutputs(h.channels[leaderIdx])
	var decide *Decide
	for _, o := range leaderOut {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgDecide {
			decide = o.Message.Payload.(*Decide)
		}
		if o.Type == OutputBlockCommitted {
			h.committed = append(h.committed, struct {
				view ViewNumber
				hash types.Hash
			}{view: o.View, hash: o.Hash})
		}
	}
	if decide == nil {
		t.Fatal("majority leader did not form CommitQC; SR5 (quorum=5/7) failed")
	}

	// Route Decide only inside majority.
	for i := 0; i < n; i++ {
		if i == leaderIdx || !majority[i] {
			continue
		}
		_ = h.engines[i].ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgDecide, Payload: decide},
		})
	}

	// Drain everyone so per-engine output channels don't fill up before assertions.
	h.drainAll()

	// Majority engines should have advanced to view 2.
	for i := 0; i < n; i++ {
		if !majority[i] {
			continue
		}
		if cv := h.engines[i].CurrentView(); cv != 2 {
			t.Errorf("majority engine %d at view %d, want 2", i, cv)
		}
	}

	// Minority engines should remain at view 1 — they never received the
	// Proposal, PrepareQC, or Decide.
	for i := range minority {
		if cv := h.engines[i].CurrentView(); cv != 1 {
			t.Errorf("minority engine %d at view %d, want 1 (no message ever delivered)", i, cv)
		}
	}

	// Verify that view 1 was committed at least once (the leader).
	var view1Committed bool
	for _, c := range h.committed {
		if c.view == 1 {
			view1Committed = true
		}
	}
	if !view1Committed {
		t.Fatal("view 1 was not committed in the majority partition")
	}
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// countOutputs counts EngineOutput entries matching the given output type and,
// if non-zero, message type.
func countOutputs(outs []EngineOutput, ot EngineOutputType, mt ConsensusMsgType) int {
	count := 0
	for _, o := range outs {
		if o.Type != ot {
			continue
		}
		if mt != 0 && (o.Message == nil || o.Message.Type != mt) {
			continue
		}
		count++
	}
	return count
}
