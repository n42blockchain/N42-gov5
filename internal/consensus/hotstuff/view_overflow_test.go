// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// AM34: a QC or TC for view 2^64-1 is invalid, and view_after_tc is defined
// only below it. These tests pin that gov5 no longer wraps to view 0.

func TestViewAfterTimeoutNeverWraps(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		for _, tenure := range []uint64{1, 8} {
			if got := ViewAfterTimeout(MaxCertView, tenure, 0, rotate); got != MaxCertView {
				t.Fatalf("ViewAfterTimeout(2^64-1, tenure=%d, rotate=%v) = %d, want no wrap", tenure, rotate, got)
			}
		}
	}
	if got := ViewAfterTimeout(MaxCertView-1, 1, 0, false); got != MaxCertView {
		t.Fatalf("ViewAfterTimeout(2^64-2) = %d, want 2^64-1", got)
	}
}

func TestVerifyRejectsCertForLastView(t *testing.T) {
	setup := newTestSetup(t, 4)
	tc := buildTestTC(t, setup, MaxCertView, 0)
	if err := VerifyTC(tc, setup.vs); err == nil {
		t.Fatal("TC for view 2^64-1 verified")
	} else if _, ok := err.(*InvalidTCError); !ok {
		t.Fatalf("want InvalidTCError, got %T %v", err, err)
	}

	hash := types.Hash{0xaa}
	vc := NewVoteCollector(MaxCertView, hash, setup.vs.Len())
	for i := 0; i < setup.vs.QuorumSize(); i++ {
		_ = vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(SigningMessage(MaxCertView, hash)))
	}
	qc, err := vc.BuildQC(setup.vs)
	if err != nil {
		t.Fatal(err)
	}
	for name, verr := range map[string]error{
		"VerifyQC":          VerifyQC(qc, setup.vs),
		"VerifyQCAnyDomain": VerifyQCAnyDomain(qc, setup.vs),
	} {
		if _, ok := verr.(*InvalidQCError); !ok {
			t.Fatalf("%s: want InvalidQCError for view 2^64-1, got %v", name, verr)
		}
	}

	// The view just below stays certifiable.
	if err := VerifyTC(buildTestTC(t, setup, MaxCertView-1, 0), setup.vs); err != nil {
		t.Fatalf("TC for view 2^64-2 rejected: %v", err)
	}
}

// A piggybacked TC for the last view neither moves the engine nor becomes its
// highest TC (before AM34 it wrapped to target 0 and was recorded unverified).
func TestEmbeddedTCForLastViewIgnored(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.roundState.AdvanceView(5)
	tc := buildTestTC(t, setup, MaxCertView, 0)
	tm := &TimeoutMessage{View: 5, HighQC: GenesisQC(), Sender: 1,
		Signature: setup.keys[1].Sign(TimeoutSigningMessage(5)).Marshal(), HighTC: tc}
	_ = e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgTimeout, Payload: tm}})
	if got := e.CurrentView(); got != 5 {
		t.Fatalf("view moved to %d", got)
	}
	if h := e.roundState.HighestTC(); h != nil && h.View == MaxCertView {
		t.Fatal("TC for view 2^64-1 recorded as highest TC")
	}
}

func TestNewViewWithLastViewTCRejected(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.roundState.AdvanceView(5)
	tc := buildTestTC(t, setup, MaxCertView, 0)
	for _, view := range []ViewNumber{0, MaxCertView} {
		leader := e.LeaderForView(view, setup.vs)
		nv := &NewViewMsg{View: view, TimeoutCert: *tc, Leader: leader,
			Signature: setup.keys[leader].Sign(NewViewSigningMessage(view)).Marshal()}
		_ = e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgNewView, Payload: nv}})
		if got := e.CurrentView(); got != 5 {
			t.Fatalf("NewView(view=%d) with TC 2^64-1 moved engine to %d", view, got)
		}
	}
	nv := &NewViewMsg{View: MaxCertView, TimeoutCert: *tc, Leader: e.LeaderForView(MaxCertView, setup.vs)}
	nv.Signature = setup.keys[nv.Leader].Sign(NewViewSigningMessage(MaxCertView)).Marshal()
	if err := e.processNewView(nv); err == nil {
		t.Fatal("processNewView accepted a TC for view 2^64-1")
	}
}

// Timeouts for the last view are refused, and no timeout is signed there.
func TestNoTimeoutAtLastView(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, out := newTestEngine(t, setup, 0)
	e.roundState.AdvanceView(MaxCertView)
	tm := &TimeoutMessage{View: MaxCertView, HighQC: GenesisQC(), Sender: 1,
		Signature: setup.keys[1].Sign(TimeoutSigningMessage(MaxCertView)).Marshal()}
	if err := e.processTimeout(tm); err == nil {
		t.Fatal("timeout for view 2^64-1 accepted")
	}
	drainOutputs(out)
	e.pacemaker.ResetForView(MaxCertView, 0)
	e.pacemaker.ExtendDeadline(-time.Hour)
	if err := e.onTimeout(); err != nil {
		t.Fatal(err)
	}
	for _, o := range drainOutputs(out) {
		if o.Message != nil && o.Message.Type == MsgTimeout {
			t.Fatal("signed a timeout for view 2^64-1")
		}
	}
}
