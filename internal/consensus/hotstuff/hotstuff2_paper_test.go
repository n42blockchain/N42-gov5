// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func buildPaperTestQC(t *testing.T, setup *testSetup, view ViewNumber, blockHash types.Hash) *QuorumCertificate {
	t.Helper()

	vc := NewVoteCollector(view, blockHash, setup.vs.Len())
	msg := SigningMessage(view, blockHash)
	for i := 0; i < setup.vs.QuorumSize(); i++ {
		if err := vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(msg)); err != nil {
			t.Fatalf("add prepare vote %d: %v", i, err)
		}
	}
	qc, err := vc.BuildQC(setup.vs)
	if err != nil {
		t.Fatalf("build prepare QC: %v", err)
	}
	return qc
}

func buildPaperTestCommitQC(t *testing.T, setup *testSetup, view ViewNumber, blockHash types.Hash) *QuorumCertificate {
	t.Helper()

	vc := NewVoteCollector(view, blockHash, setup.vs.Len())
	msg := CommitSigningMessage(view, blockHash)
	for i := 0; i < setup.vs.QuorumSize(); i++ {
		if err := vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(msg)); err != nil {
			t.Fatalf("add commit vote %d: %v", i, err)
		}
	}
	qc, err := vc.BuildQCWithMessage(setup.vs, msg)
	if err != nil {
		t.Fatalf("build commit QC: %v", err)
	}
	return qc
}

func buildPaperTestTC(t *testing.T, setup *testSetup, view ViewNumber, highQC QuorumCertificate) *TimeoutCertificate {
	t.Helper()

	tc := NewTimeoutCollector(view, setup.vs.Len())
	msg := TimeoutSigningMessage(view)
	for i := 0; i < setup.vs.QuorumSize(); i++ {
		if err := tc.AddVerifiedTimeout(ValidatorIndex(i), setup.keys[i].Sign(msg), highQC.Clone()); err != nil {
			t.Fatalf("add timeout %d: %v", i, err)
		}
	}
	cert, err := tc.BuildTC(setup.vs)
	if err != nil {
		t.Fatalf("build TC: %v", err)
	}
	return cert
}

func signPaperTestNewView(setup *testSetup, view ViewNumber, tc TimeoutCertificate) *NewViewMsg {
	leader := LeaderForView(view, setup.vs)
	sig := setup.keys[leader].Sign(NewViewSigningMessage(view))
	return &NewViewMsg{
		View:        view,
		TimeoutCert: tc,
		Leader:      leader,
		Signature:   sig.Marshal(),
	}
}

func TestHotStuff2NewViewInstallsVerifiedHighQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)

	highHash := types.Hash{0x11, 0x22}
	highQC := buildPaperTestQC(t, setup, 1, highHash)
	tc := buildPaperTestTC(t, setup, 1, highQC.Clone())
	newView := signPaperTestNewView(setup, 2, *tc)

	if err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgNewView, Payload: newView},
	}); err != nil {
		t.Fatalf("process NewView: %v", err)
	}

	if got := follower.CurrentView(); got != 2 {
		t.Fatalf("current view = %d, want 2", got)
	}
	locked := follower.LockedQC()
	if locked.View != highQC.View || locked.BlockHash != highHash {
		t.Fatalf("locked QC = (view %d, hash %s), want (view %d, hash %s)",
			locked.View, locked.BlockHash.Hex(), highQC.View, highHash.Hex())
	}
	if countOutputs(drainOutputs(outputCh), OutputViewChanged, 0) != 1 {
		t.Fatal("NewView should emit exactly one view-change output")
	}
}

func TestHotStuff2RejectsNewViewWithInvalidHighQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)

	tc := buildPaperTestTC(t, setup, 1, GenesisQC())
	tc.HighQC = QuorumCertificate{
		View:               1,
		BlockHash:          types.Hash{0x99},
		AggregateSignature: []byte{0x01, 0x02, 0x03},
		Signers:            []bool{true, true, true, false},
	}
	newView := signPaperTestNewView(setup, 2, *tc)

	err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgNewView, Payload: newView},
	})
	if err == nil {
		t.Fatal("NewView with invalid embedded high_qc should be rejected")
	}
	if _, ok := err.(*InvalidQCError); !ok {
		t.Fatalf("expected InvalidQCError, got %T: %v", err, err)
	}
	if got := follower.CurrentView(); got != 1 {
		t.Fatalf("current view changed to %d after invalid NewView", got)
	}
	if locked := follower.LockedQC(); locked.View != 0 {
		t.Fatalf("locked QC changed to view %d after invalid NewView", locked.View)
	}
	if got := len(drainOutputs(outputCh)); got != 0 {
		t.Fatalf("invalid NewView emitted %d output(s)", got)
	}
}

func TestHotStuff2RejectsTimeoutWithInvalidHighQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newTestEngine(t, setup, 0)

	timeoutSig := setup.keys[1].Sign(TimeoutSigningMessage(1))
	timeoutMsg := &TimeoutMessage{
		View: 1,
		HighQC: QuorumCertificate{
			View:               1,
			BlockHash:          types.Hash{0xAA},
			AggregateSignature: []byte{0x01, 0x02, 0x03},
			Signers:            []bool{true, true, true, false},
		},
		Sender:    1,
		Signature: timeoutSig.Marshal(),
	}

	err := engine.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgTimeout, Payload: timeoutMsg},
	})
	if err == nil {
		t.Fatal("TimeoutMessage with invalid high_qc should be rejected")
	}
	if _, ok := err.(*InvalidQCError); !ok {
		t.Fatalf("expected InvalidQCError, got %T: %v", err, err)
	}
	if got := engine.CurrentView(); got != 1 {
		t.Fatalf("current view changed to %d after invalid timeout", got)
	}
	if got := len(drainOutputs(outputCh)); got != 0 {
		t.Fatalf("invalid timeout emitted %d output(s)", got)
	}
}

func TestHotStuff2RejectsDecideWithPrepareDomainQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)

	blockHash := types.Hash{0x33}
	prepareQC := buildPaperTestQC(t, setup, 1, blockHash)
	decide := &Decide{
		View:      1,
		BlockHash: blockHash,
		CommitQC:  *prepareQC,
	}

	err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgDecide, Payload: decide},
	})
	if err == nil {
		t.Fatal("Decide with prepare-domain QC should be rejected")
	}
	if _, ok := err.(*InvalidQCError); !ok {
		t.Fatalf("expected InvalidQCError, got %T: %v", err, err)
	}
	if got := follower.CurrentView(); got != 1 {
		t.Fatalf("current view changed to %d after invalid Decide", got)
	}
	if countOutputs(drainOutputs(outputCh), OutputBlockCommitted, 0) != 0 {
		t.Fatal("invalid Decide must not emit BlockCommitted")
	}
}

func TestHotStuff2RejectsDecideBlockHashMismatch(t *testing.T) {
	setup := newTestSetup(t, 4)
	follower, outputCh := newTestEngine(t, setup, 0)

	qcHash := types.Hash{0x44}
	decideHash := types.Hash{0x45}
	commitQC := buildPaperTestCommitQC(t, setup, 1, qcHash)
	decide := &Decide{
		View:      1,
		BlockHash: decideHash,
		CommitQC:  *commitQC,
	}

	err := follower.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgDecide, Payload: decide},
	})
	if err == nil {
		t.Fatal("Decide with mismatched CommitQC block hash should be rejected")
	}
	if _, ok := err.(*BlockHashMismatchError); !ok {
		t.Fatalf("expected BlockHashMismatchError, got %T: %v", err, err)
	}
	if got := follower.CurrentView(); got != 1 {
		t.Fatalf("current view changed to %d after invalid Decide", got)
	}
	if countOutputs(drainOutputs(outputCh), OutputBlockCommitted, 0) != 0 {
		t.Fatal("invalid Decide must not emit BlockCommitted")
	}
}

func TestHotStuff2FutureMessageBufferCap(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	for i := 0; i < MaxFutureMessages+25; i++ {
		view := ViewNumber(2 + (i % FutureViewWindow))
		vote := &Vote{
			View:      view,
			BlockHash: types.Hash{byte(i)},
			Voter:     1,
			Signature: []byte{byte(i)},
		}
		if err := engine.ProcessEvent(ConsensusEvent{
			Type: EventMessage,
			Msg:  ConsensusMsg{Type: MsgVote, Payload: vote},
		}); err != nil {
			t.Fatalf("future vote %d: %v", i, err)
		}
	}

	engine.mu.Lock()
	buffered := len(engine.futureMsgBuffer)
	engine.mu.Unlock()

	if buffered != MaxFutureMessages {
		t.Fatalf("future message buffer len = %d, want cap %d", buffered, MaxFutureMessages)
	}
	if got := engine.CurrentView(); got != 1 {
		t.Fatalf("future messages changed current view to %d", got)
	}
}

func TestHotStuff2FutureMessageBeyondWindowWithoutQCIgnored(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, outputCh := newTestEngine(t, setup, 0)

	vote := &Vote{
		View:      ViewNumber(1 + FutureViewWindow + 1),
		BlockHash: types.Hash{0x66},
		Voter:     1,
		Signature: []byte{0x01},
	}
	if err := engine.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgVote, Payload: vote},
	}); err != nil {
		t.Fatalf("far-future vote: %v", err)
	}

	engine.mu.Lock()
	buffered := len(engine.futureMsgBuffer)
	engine.mu.Unlock()

	if buffered != 0 {
		t.Fatalf("far-future message without QC buffered %d entries", buffered)
	}
	if got := engine.CurrentView(); got != 1 {
		t.Fatalf("far-future message changed current view to %d", got)
	}
	if got := len(drainOutputs(outputCh)); got != 0 {
		t.Fatalf("far-future message emitted %d output(s)", got)
	}
}
