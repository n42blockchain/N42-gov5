// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the SyncInfo-style TC piggyback (Jolteon/Aptos view sync):
// votes/timeouts carry the highest known TimeoutCertificate so a lagging
// validator jumps to the network's view without waiting for a NewView.

package hotstuff

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// buildRealTC builds a genuinely BLS-signed TC for the given view using the
// first quorumSize validators of setup, with a genesis HighQC.
func buildRealTC(t *testing.T, setup *testSetup, view ViewNumber) *TimeoutCertificate {
	t.Helper()
	tc := NewTimeoutCollector(view, setup.vs.Len())
	genesis := GenesisQC()
	msg := TimeoutSigningMessage(view)
	for i := 0; i < setup.vs.QuorumSize(); i++ {
		sig := setup.keys[i].Sign(msg)
		if err := tc.AddVerifiedTimeout(ValidatorIndex(i), sig, genesis); err != nil {
			t.Fatalf("add timeout %d: %v", i, err)
		}
	}
	tcert, err := tc.BuildTC(setup.vs)
	if err != nil {
		t.Fatalf("BuildTC: %v", err)
	}
	if err := VerifyTC(tcert, setup.vs); err != nil {
		t.Fatalf("VerifyTC: %v", err)
	}
	return tcert
}

// ============================================================================
// Codec round-trip: HighTC piggyback field
// ============================================================================

func TestEncodeDecodeVote_WithHighTC(t *testing.T) {
	tc := makeTestTC()
	original := &ConsensusMsg{
		Type: MsgVote,
		Payload: &Vote{
			View:      9,
			BlockHash: types.Hash{0xBE, 0xEF},
			Voter:     1,
			Signature: bytes.Repeat([]byte{0x77}, 96),
			HighTC:    &tc,
		},
	}

	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	v := decoded.Payload.(*Vote)
	if v.HighTC == nil {
		t.Fatal("HighTC lost in round-trip")
	}
	if v.HighTC.View != tc.View {
		t.Fatalf("HighTC view: got %d want %d", v.HighTC.View, tc.View)
	}
	if v.HighTC.HighQC.View != tc.HighQC.View {
		t.Fatalf("HighTC.HighQC view: got %d want %d", v.HighTC.HighQC.View, tc.HighQC.View)
	}
	if !bytes.Equal(v.HighTC.AggregateSignature, tc.AggregateSignature) {
		t.Fatal("HighTC signature mismatch")
	}
}

func TestEncodeDecodeVote_NilHighTC(t *testing.T) {
	original := &ConsensusMsg{
		Type: MsgVote,
		Payload: &Vote{
			View:      3,
			BlockHash: types.Hash{0xBE, 0xEF},
			Voter:     1,
			Signature: bytes.Repeat([]byte{0x77}, 96),
			// HighTC left nil
		},
	}
	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Payload.(*Vote).HighTC != nil {
		t.Fatal("expected nil HighTC")
	}
}

func TestEncodeDecodeTimeout_WithHighTC(t *testing.T) {
	tc := makeTestTC()
	original := &ConsensusMsg{
		Type: MsgTimeout,
		Payload: &TimeoutMessage{
			View:      12,
			HighQC:    makeTestQC(),
			Sender:    2,
			Signature: bytes.Repeat([]byte{0x99}, 96),
			HighTC:    &tc,
		},
	}
	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	tm := decoded.Payload.(*TimeoutMessage)
	if tm.HighQC.View != 5 {
		t.Fatalf("HighQC view: got %d", tm.HighQC.View)
	}
	if tm.HighTC == nil || tm.HighTC.View != tc.View {
		t.Fatal("HighTC lost in round-trip")
	}
}

// ============================================================================
// RoundState.highestTC tracking
// ============================================================================

func TestHighestTCTracking(t *testing.T) {
	rs := NewRoundState()
	if rs.HighestTC() != nil {
		t.Fatal("fresh round state should have no TC")
	}

	tc5 := &TimeoutCertificate{View: 5}
	rs.UpdateHighestTC(tc5)
	if rs.HighestTC() == nil || rs.HighestTC().View != 5 {
		t.Fatal("should hold TC view 5")
	}

	// Lower view is ignored.
	rs.UpdateHighestTC(&TimeoutCertificate{View: 3})
	if rs.HighestTC().View != 5 {
		t.Fatal("lower TC must not overwrite")
	}

	// Higher view wins.
	rs.UpdateHighestTC(&TimeoutCertificate{View: 8})
	if rs.HighestTC().View != 8 {
		t.Fatal("higher TC should win")
	}

	// nil is a no-op.
	rs.UpdateHighestTC(nil)
	if rs.HighestTC().View != 8 {
		t.Fatal("nil must not clear")
	}

	// Stored copy is independent of the caller's TC.
	src := &TimeoutCertificate{View: 20, AggregateSignature: []byte{1, 2, 3}}
	rs.UpdateHighestTC(src)
	src.View = 99
	src.AggregateSignature[0] = 0xFF
	if rs.HighestTC().View != 20 || rs.HighestTC().AggregateSignature[0] != 1 {
		t.Fatal("UpdateHighestTC must store an independent clone")
	}
}

// ============================================================================
// Engine: SyncInfo TC view jump
// ============================================================================

// TestSyncInfoTCViewJump verifies a validator stuck at a low view jumps to
// tc.View+1 when it receives a message carrying a valid piggybacked TC — even
// though it never saw the corresponding NewView.
func TestSyncInfoTCViewJump(t *testing.T) {
	setup := newTestSetup(t, 4)

	// A real TC for view 5 → the network is at view 6.
	tcert := buildRealTC(t, setup, 5)

	// Pick a node that is NOT the leader of view 6 so the trailing vote
	// dispatch is a clean no-op after the jump.
	leader6 := LeaderForView(6, setup.vs)
	myIndex := int((leader6 + 1) % 4)

	engine, outputCh := newTestEngine(t, setup, myIndex)
	if engine.roundState.CurrentView() != 1 {
		t.Fatalf("engine should start at view 1, got %d", engine.roundState.CurrentView())
	}

	// A vote (any view) carrying the piggybacked TC.
	msg := ConsensusMsg{
		Type: MsgVote,
		Payload: &Vote{
			View:      6,
			BlockHash: types.Hash{0xAB},
			Voter:     0,
			Signature: bytes.Repeat([]byte{0x01}, 96),
			HighTC:    tcert,
		},
	}
	if err := engine.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: msg}); err != nil {
		t.Fatalf("ProcessEvent: %v", err)
	}

	if got := engine.roundState.CurrentView(); got != 6 {
		t.Fatalf("expected view jump to 6, got %d", got)
	}
	// The adopted TC becomes the node's highest TC for re-propagation.
	if engine.roundState.HighestTC() == nil || engine.roundState.HighestTC().View != 5 {
		t.Fatal("engine should adopt the piggybacked TC as highestTC")
	}

	// A ViewChanged output must have been emitted.
	sawViewChanged := false
	for _, o := range drainOutputs(outputCh) {
		if o.Type == OutputViewChanged && o.View == 6 {
			sawViewChanged = true
		}
	}
	if !sawViewChanged {
		t.Fatal("expected OutputViewChanged(view=6)")
	}
}

// TestSyncInfoTCNoJumpBackwards verifies a stale piggybacked TC never moves the
// view backwards and never gets adopted as the highest TC over a fresher one.
func TestSyncInfoTCNoJumpBackwards(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	// Advance the engine to view 10 first via a fresh TC (view 9).
	tc9 := buildRealTC(t, setup, 9)
	msg := ConsensusMsg{
		Type:    MsgTimeout,
		Payload: &TimeoutMessage{View: 10, HighQC: GenesisQC(), Sender: 1, Signature: setup.keys[1].Sign(TimeoutSigningMessage(10)).Marshal(), HighTC: tc9},
	}
	if err := engine.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: msg}); err != nil {
		t.Fatalf("ProcessEvent (advance): %v", err)
	}
	if engine.roundState.CurrentView() != 10 {
		t.Fatalf("expected view 10, got %d", engine.roundState.CurrentView())
	}

	// Now feed a stale TC (view 5) — must not move the view backwards.
	staleTC := buildRealTC(t, setup, 5)
	staleMsg := ConsensusMsg{
		Type:    MsgVote,
		Payload: &Vote{View: 6, BlockHash: types.Hash{0x1}, Voter: 0, Signature: bytes.Repeat([]byte{0x01}, 96), HighTC: staleTC},
	}
	if err := engine.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: staleMsg}); err != nil {
		t.Fatalf("ProcessEvent (stale): %v", err)
	}
	if engine.roundState.CurrentView() != 10 {
		t.Fatalf("stale TC moved the view: got %d", engine.roundState.CurrentView())
	}
	if engine.roundState.HighestTC().View != 9 {
		t.Fatalf("stale TC overwrote highestTC: got %d", engine.roundState.HighestTC().View)
	}
}

// buildRealQC builds a genuinely BLS-signed prepare-domain QC for (view,
// blockHash) using the first quorumSize validators of setup.
func buildRealQC(t *testing.T, setup *testSetup, view ViewNumber, blockHash types.Hash) *QuorumCertificate {
	t.Helper()
	vc := NewVoteCollector(view, blockHash, setup.vs.Len())
	msg := SigningMessage(view, blockHash)
	for i := 0; i < setup.vs.QuorumSize(); i++ {
		if err := vc.AddVerifiedVoteFromBytes(ValidatorIndex(i), setup.keys[i].Sign(msg).Marshal()); err != nil {
			t.Fatalf("add vote %d: %v", i, err)
		}
	}
	qc, err := vc.BuildQC(setup.vs)
	if err != nil {
		t.Fatalf("BuildQC: %v", err)
	}
	if err := VerifyQC(qc, setup.vs); err != nil {
		t.Fatalf("VerifyQC: %v", err)
	}
	return qc
}

// TestQCViewJumpBoundToQCView is the security regression for the tryQCViewJump
// fix: a QC only proves the network reached qc.View, so a message carrying a
// real QC but an arbitrarily high (attacker-chosen, unauthenticated) view must
// advance the node only to qc.View+1 — never to the claimed msgView. Before the
// fix, wrapping a public QC in a Proposal with a garbage signature and a huge
// view pushed the node's view to that height (a persisted liveness DoS).
func TestQCViewJumpBoundToQCView(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	// A genuine QC for view 3 → the network legitimately reached view 3.
	qc3 := buildRealQC(t, setup, 3, types.Hash{0xAB})

	// Attacker wraps it in a proposal claiming an absurd view, with a garbage
	// (never-verified-before-the-jump) signature.
	const bogusView = ViewNumber(1_000_000)
	msg := ConsensusMsg{
		Type: MsgProposal,
		Payload: &Proposal{
			View:      bogusView,
			BlockHash: types.Hash{0xCD},
			JustifyQC: *qc3,
			Proposer:  0,
			Signature: bytes.Repeat([]byte{0xFF}, 96),
		},
	}
	if err := engine.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: msg}); err != nil {
		t.Fatalf("ProcessEvent: %v", err)
	}

	// The node must sit at qc.View+1 = 4, NOT the attacker's 1_000_000.
	if got := engine.roundState.CurrentView(); got != 4 {
		t.Fatalf("view = %d, want 4 (qc.View+1); attacker-chosen msgView must not advance the node", got)
	}
}
