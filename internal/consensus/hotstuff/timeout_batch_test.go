// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func tcSigners(tc *TimeoutCertificate) int {
	n := 0
	for _, s := range tc.Signers {
		if s {
			n++
		}
	}
	return n
}

// signedTimeout is validator i's timeout for view, signed by signer's key
// (signer != i forges i's slot).
func signedTimeout(e *ConsensusEngine, setup *testSetup, view ViewNumber, i, signer int) *TimeoutMessage {
	return &TimeoutMessage{
		View:      view,
		HighQC:    GenesisQC(),
		Sender:    ValidatorIndex(i),
		Signature: setup.keys[signer].Sign(e.timeoutSigningMessage(view)).Marshal(),
	}
}

// TestTimeoutBatchAllValidOnePairing: n valid timeouts reach the TC with a
// single pairing check and no per-message verification.
func TestTimeoutBatchAllValidOnePairing(t *testing.T) {
	setup := newTestSetup(t, 10)
	e, _ := newTestEngine(t, setup, 0)
	view := e.CurrentView()
	q := setup.vs.QuorumSize()
	n := pairingsOf(func() {
		for i := 1; i <= q; i++ {
			if err := e.processTimeout(signedTimeout(e, setup, view, i, i)); err != nil {
				t.Fatalf("timeout %d: %v", i, err)
			}
		}
	})
	if e.CurrentView() <= view {
		t.Fatalf("no TC: view %d", e.CurrentView())
	}
	if n != 1 {
		t.Fatalf("%d valid timeouts cost %d pairing checks, want 1", q, n)
	}
	tc := e.roundState.HighestTC()
	if tc == nil || tc.View != view || tcSigners(tc) != q {
		t.Fatalf("TC %+v, want view %d with %d signers", tc, view, q)
	}
	if err := VerifyTC(tc, setup.vs); err != nil {
		t.Fatalf("emitted TC does not verify: %v", err)
	}
}

// TestTimeoutBatchOneBadAmongN: one forged timeout among n is isolated by
// bisection and excluded; the TC forms from the valid ones and verifies.
func TestTimeoutBatchOneBadAmongN(t *testing.T) {
	setup := newTestSetup(t, 10) // f = 3, quorum 7
	e, _ := newTestEngine(t, setup, 0)
	view := e.CurrentView()
	q := setup.vs.QuorumSize()
	const bad = 4
	// q timeouts, one of them forged: the batch fails, the valid ones
	// stay below quorum, so no TC yet and no error.
	for i := 1; i <= q; i++ {
		signer := i
		if i == bad {
			signer = 9 // validator 9's key signs in validator 4's slot
		}
		if err := e.processTimeout(signedTimeout(e, setup, view, i, signer)); err != nil {
			t.Fatalf("timeout %d: %v", i, err)
		}
	}
	if e.CurrentView() != view {
		t.Fatalf("TC formed with a forged timeout counted (view %d)", e.CurrentView())
	}
	if e.timeoutCollector.TimeoutCount() != q-1 {
		t.Fatalf("collector holds %d timeouts, want %d (forged one dropped)", e.timeoutCollector.TimeoutCount(), q-1)
	}
	// One more valid timeout completes the quorum.
	if err := e.processTimeout(signedTimeout(e, setup, view, q+1, q+1)); err != nil {
		t.Fatal(err)
	}
	if e.CurrentView() <= view {
		t.Fatalf("no TC after the quorum of valid timeouts")
	}
	tc := e.roundState.HighestTC()
	if tc.Signers[bad] {
		t.Fatal("forged signer counted in the TC")
	}
	if tcSigners(tc) != q {
		t.Fatalf("TC signers %d, want %d", tcSigners(tc), q)
	}
	if err := VerifyTC(tc, setup.vs); err != nil {
		t.Fatalf("TC does not verify: %v", err)
	}
}

// TestTimeoutBatchForgedSlotReplaced: a forged timeout squatting on a
// validator's slot does not block that validator's genuine timeout.
func TestTimeoutBatchForgedSlotReplaced(t *testing.T) {
	setup := newTestSetup(t, 4) // quorum 3
	e, _ := newTestEngine(t, setup, 0)
	view := e.CurrentView()
	if err := e.processTimeout(signedTimeout(e, setup, view, 1, 2)); err != nil { // forged slot 1
		t.Fatal(err)
	}
	if err := e.processTimeout(signedTimeout(e, setup, view, 1, 1)); err != nil { // genuine slot 1
		t.Fatal(err)
	}
	if err := e.processTimeout(signedTimeout(e, setup, view, 2, 2)); err != nil {
		t.Fatal(err)
	}
	if err := e.processTimeout(signedTimeout(e, setup, view, 3, 3)); err != nil {
		t.Fatal(err)
	}
	if e.CurrentView() <= view {
		t.Fatal("forged timeout blocked the genuine one: no TC")
	}
	tc := e.roundState.HighestTC()
	if !tc.Signers[1] {
		t.Fatal("genuine timeout of validator 1 not in the TC")
	}
	if err := VerifyTC(tc, setup.vs); err != nil {
		t.Fatal(err)
	}
}

// TestTimeoutBatchRepeatNoCrypto: a byte-identical repeat costs nothing.
func TestTimeoutBatchRepeatNoCrypto(t *testing.T) {
	setup := newTestSetup(t, 7)
	e, _ := newTestEngine(t, setup, 0)
	view := e.CurrentView()
	h := types.Hash{0x11}
	tm := signedTimeout(e, setup, view, 2, 2)
	tm.HighQC = buildSignedQC(setup.keys, view, h, e.voteSigningMessage(view, h), setup.vs.QuorumSize(), 7)
	if err := e.processTimeout(tm); err != nil {
		t.Fatal(err)
	}
	if n := pairingsOf(func() {
		for i := 0; i < 5; i++ {
			if err := e.processTimeout(tm); err != nil {
				t.Fatal(err)
			}
		}
	}); n != 0 {
		t.Fatalf("repeats cost %d pairing checks, want 0", n)
	}
}

// TestTimeoutCollectorVerifyPendingBisects exercises the collector alone:
// two bad signatures among 20 are both found.
func TestTimeoutCollectorVerifyPendingBisects(t *testing.T) {
	setup := newTestSetup(t, 20)
	msg := TimeoutSigningMessage(9)
	tc := NewTimeoutCollector(9, 20)
	for i := 0; i < 20; i++ {
		signer := i
		if i == 3 || i == 17 {
			signer = (i + 1) % 20
		}
		sig := setup.keys[signer].Sign(msg)
		if err := tc.AddPendingTimeout(ValidatorIndex(i), sig, sig.Marshal(), GenesisQC(), setup.vs, msg); err != nil {
			t.Fatal(err)
		}
	}
	bad := tc.verifyPending(setup.vs, msg)
	if len(bad) != 2 {
		t.Fatalf("found %v, want validators 3 and 17", bad)
	}
	got := map[ValidatorIndex]bool{bad[0]: true, bad[1]: true}
	if !got[3] || !got[17] {
		t.Fatalf("found %v, want 3 and 17", bad)
	}
	cert, err := tc.BuildTCWithMessages(setup.vs, msg, func(*QuorumCertificate) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if tcSigners(cert) != 18 || cert.Signers[3] || cert.Signers[17] {
		t.Fatalf("TC signers %d (3:%v 17:%v), want 18 without 3 and 17", tcSigners(cert), cert.Signers[3], cert.Signers[17])
	}
	if err := VerifyTC(cert, setup.vs); err != nil {
		t.Fatal(err)
	}
	// Below quorum after drops: InsufficientVotesError.
	small := NewTimeoutCollector(9, 20)
	for i := 0; i < setup.vs.QuorumSize(); i++ {
		sig := setup.keys[(i+1)%20].Sign(msg) // all forged
		_ = small.AddPendingTimeout(ValidatorIndex(i), sig, sig.Marshal(), GenesisQC(), setup.vs, msg)
	}
	_, err = small.BuildTCWithMessages(setup.vs, msg, func(*QuorumCertificate) error { return nil })
	var short *InsufficientVotesError
	if !errors.As(err, &short) {
		t.Fatalf("all-forged collector: err %v, want InsufficientVotesError", err)
	}
}
