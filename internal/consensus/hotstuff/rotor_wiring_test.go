// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// TestMessageSenderIndex covers every message type that carries an explicit
// sender/proposer validator index, plus the types that don't (PrepareQCMsg,
// Decide have no single-signer field) and the nil case.
func TestMessageSenderIndex(t *testing.T) {
	cases := []struct {
		name    string
		msg     *ConsensusMsg
		wantIdx ValidatorIndex
		wantOK  bool
	}{
		{"nil message", nil, 0, false},
		{"proposal", &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{Proposer: 3}}, 3, true},
		{"vote", &ConsensusMsg{Type: MsgVote, Payload: &Vote{Voter: 2}}, 2, true},
		{"commit vote", &ConsensusMsg{Type: MsgCommitVote, Payload: &CommitVote{Voter: 5}}, 5, true},
		{"timeout", &ConsensusMsg{Type: MsgTimeout, Payload: &TimeoutMessage{Sender: 1}}, 1, true},
		{"new view", &ConsensusMsg{Type: MsgNewView, Payload: &NewViewMsg{Leader: 4}}, 4, true},
		{"prepare qc has no signer", &ConsensusMsg{Type: MsgPrepareQC, Payload: &PrepareQCMsg{}}, 0, false},
		{"decide has no signer", &ConsensusMsg{Type: MsgDecide, Payload: &Decide{}}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx, ok := messageSenderIndex(tc.msg)
			if ok != tc.wantOK || (ok && idx != tc.wantIdx) {
				t.Fatalf("messageSenderIndex(%+v) = (%d, %v), want (%d, %v)", tc.msg, idx, ok, tc.wantIdx, tc.wantOK)
			}
		})
	}
}

// newRotorWiringService builds a minimal Service wired to a real ConsensusEngine
// and validator set (no P2P, no DB) — enough to exercise learnValidatorPeer and
// prove Rotor's peer registry, which prior to this fix was NEVER populated in
// production, actually gets filled from consensus traffic.
func newRotorWiringService(t *testing.T, n int) (*Service, *testSetup) {
	t.Helper()
	setup := newTestSetup(t, n)
	ce, _ := newTestEngine(t, setup, 0)
	return &Service{
		engine: &HotStuff{engine: ce},
		rotor:  NewRotor(3),
	}, setup
}

// TestLearnValidatorPeerRegistersMapping is the core regression test for the
// Rotor wiring fix: before this change, RegisterValidator was dead code outside
// tests, so LookupPeer always missed and every targeted send fell back to full
// gossip broadcast. This proves a processed Vote now teaches Rotor the sender's
// peer.ID, and LookupPeer resolves it afterward.
func TestLearnValidatorPeerRegistersMapping(t *testing.T) {
	svc, setup := newRotorWiringService(t, 4)
	const senderIdx = ValidatorIndex(2)
	const from = peer.ID("peer-2")

	if _, ok := svc.rotor.LookupPeer(setup.vs, senderIdx); ok {
		t.Fatal("registry should start empty")
	}

	svc.learnValidatorPeer(&ConsensusMsg{Type: MsgVote, Payload: &Vote{Voter: senderIdx}}, from)

	pid, ok := svc.rotor.LookupPeer(setup.vs, senderIdx)
	if !ok || pid != from {
		t.Fatalf("LookupPeer(%d) = (%q, %v), want (%q, true)", senderIdx, pid, ok, from)
	}
}

// TestLearnValidatorPeerCoversAllSignedTypes exercises each message type that
// should teach Rotor a mapping, confirming the wiring isn't Vote-only.
func TestLearnValidatorPeerCoversAllSignedTypes(t *testing.T) {
	svc, setup := newRotorWiringService(t, 4)

	msgs := []struct {
		name string
		msg  *ConsensusMsg
		idx  ValidatorIndex
	}{
		{"proposal", &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{Proposer: 1}}, 1},
		{"vote", &ConsensusMsg{Type: MsgVote, Payload: &Vote{Voter: 2}}, 2},
		{"commit vote", &ConsensusMsg{Type: MsgCommitVote, Payload: &CommitVote{Voter: 3}}, 3},
		{"timeout", &ConsensusMsg{Type: MsgTimeout, Payload: &TimeoutMessage{Sender: 0}}, 0},
	}
	for i, m := range msgs {
		from := peer.ID("peer-" + string(rune('A'+i)))
		svc.learnValidatorPeer(m.msg, from)
		pid, ok := svc.rotor.LookupPeer(setup.vs, m.idx)
		if !ok || pid != from {
			t.Fatalf("%s: LookupPeer(%d) = (%q, %v), want (%q, true)", m.name, m.idx, pid, ok, from)
		}
	}
}

// TestLearnValidatorPeerIgnoresUnknownIndex ensures a validator index outside
// the current set (e.g. stale message from a since-removed validator, or a
// decode anomaly) is not registered — GetAddress fails closed.
func TestLearnValidatorPeerIgnoresUnknownIndex(t *testing.T) {
	svc, setup := newRotorWiringService(t, 4)
	svc.learnValidatorPeer(&ConsensusMsg{Type: MsgVote, Payload: &Vote{Voter: 99}}, peer.ID("peer-x"))
	if _, ok := svc.rotor.LookupPeer(setup.vs, 99); ok {
		t.Fatal("out-of-range validator index must not be registered")
	}
}

// TestLearnValidatorPeerNilSafe covers the guard clauses: nil rotor, empty
// peer.ID, and message types that carry no sender index must all be no-ops,
// not panics.
func TestLearnValidatorPeerNilSafe(t *testing.T) {
	svc, _ := newRotorWiringService(t, 4)

	svc.learnValidatorPeer(&ConsensusMsg{Type: MsgVote, Payload: &Vote{Voter: 0}}, "") // empty peer.ID
	svc.learnValidatorPeer(&ConsensusMsg{Type: MsgDecide, Payload: &Decide{}}, peer.ID("peer-y"))
	svc.learnValidatorPeer(nil, peer.ID("peer-z"))

	noRotor := &Service{engine: svc.engine} // rotor == nil
	noRotor.learnValidatorPeer(&ConsensusMsg{Type: MsgVote, Payload: &Vote{Voter: 0}}, peer.ID("peer-w"))
}

// TestUnregisterValidatorDropsMapping pins the disconnect-cleanup contract:
// after a failed direct send the service drops the mapping via
// UnregisterValidator, and the next message from that validator re-learns it.
func TestUnregisterValidatorDropsMapping(t *testing.T) {
	svc, setup := newRotorWiringService(t, 4)
	const idx = ValidatorIndex(1)
	const oldPeer = peer.ID("peer-old")
	const newPeer = peer.ID("peer-new")

	svc.learnValidatorPeer(&ConsensusMsg{Type: MsgVote, Payload: &Vote{Voter: idx}}, oldPeer)
	addr, err := setup.vs.GetAddress(idx)
	if err != nil {
		t.Fatal(err)
	}

	svc.rotor.UnregisterValidator(addr)
	if _, ok := svc.rotor.LookupPeer(setup.vs, idx); ok {
		t.Fatal("mapping should be gone after UnregisterValidator")
	}

	// Re-learn from the validator's next message — self-healing.
	svc.learnValidatorPeer(&ConsensusMsg{Type: MsgVote, Payload: &Vote{Voter: idx}}, newPeer)
	pid, ok := svc.rotor.LookupPeer(setup.vs, idx)
	if !ok || pid != newPeer {
		t.Fatalf("re-learn failed: got (%q, %v), want (%q, true)", pid, ok, newPeer)
	}
}
