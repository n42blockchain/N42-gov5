// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

// eqProposal builds a proposal from the view's leader signed over the
// engine's proposal signing message.
func eqProposal(e *ConsensusEngine, setup *testSetup, view ViewNumber, h types.Hash) *Proposal {
	leader := e.LeaderForView(view, setup.vs)
	sig := setup.keys[leader].Sign(e.proposalSigningMessage(view, h))
	return &Proposal{View: view, BlockHash: h, JustifyQC: GenesisQC(), Proposer: leader, Signature: sig.Marshal()}
}

func deliverProposal(t *testing.T, e *ConsensusEngine, p *Proposal) {
	t.Helper()
	if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgProposal, Payload: p}}); err != nil {
		t.Fatalf("proposal %x: %v", p.BlockHash[:2], err)
	}
}

func proposalEvidence(outs []EngineOutput) []EngineOutput {
	var ev []EngineOutput
	for _, o := range outs {
		if o.Type == OutputEquivocationDetected && o.EvidenceKind == EvidenceKindProposal {
			ev = append(ev, o)
		}
	}
	return ev
}

// checkProposalEvidence stores the single evidence output and verifies both
// signatures standalone against the leader's BLS public key.
func checkProposalEvidence(t *testing.T, setup *testSetup, outs []EngineOutput, view ViewNumber, h1, h2 types.Hash) {
	t.Helper()
	evs := proposalEvidence(outs)
	if len(evs) != 1 {
		t.Fatalf("want one proposal evidence output, got %d", len(evs))
	}
	store := NewEquivocationStore(8)
	if !store.RecordOutput(evs[0]) {
		t.Fatal("evidence not stored")
	}
	ev := store.Report().Evidence[0]
	leader := LeaderForView(view, setup.vs)
	if ev.Kind != EvidenceKindProposal || ev.View != uint64(view) || ev.ValidatorIndex != uint32(leader) {
		t.Fatalf("bad evidence %+v", ev)
	}
	if ev.Hash1 != evHex(h1[:]) || ev.Hash2 != evHex(h2[:]) {
		t.Fatalf("hashes %s %s", ev.Hash1, ev.Hash2)
	}
	pk, err := bls.PublicKeyFromBytes(unhex(t, ev.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if !pk.Equals(setup.pubKeys[leader]) {
		t.Fatal("evidence public key is not the leader's")
	}
	for i, m := range []SignedMessage{ev.First, ev.Second} {
		if !VerifyBLSSignature(unhex(t, m.Signature), pk, unhex(t, m.Signed)) {
			t.Fatalf("signature %d does not verify standalone", i)
		}
	}
	if ev.First.Signed == ev.Second.Signed {
		t.Fatal("signed messages must differ")
	}
}

// Second proposal after the node already voted for the first.
func TestProposalEquivocationAfterVote(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, ch := newTestEngine(t, setup, 0)
	a, b := types.Hash{0xAA}, types.Hash{0xBB}
	pa := eqProposal(e, setup, 1, a)
	deliverProposal(t, e, pa)
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: a}); err != nil {
		t.Fatal(err)
	}
	if n := countOutputs(drainOutputs(ch), OutputSendToValidator, MsgVote); n != 1 {
		t.Fatalf("want a vote for the first proposal, got %d", n)
	}

	// A duplicate of the first proposal is not evidence.
	deliverProposal(t, e, pa)
	if ev := proposalEvidence(drainOutputs(ch)); len(ev) != 0 {
		t.Fatal("identical duplicate proposal recorded as evidence")
	}

	deliverProposal(t, e, eqProposal(e, setup, 1, b))
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: b}); err != nil {
		t.Fatal(err)
	}
	outs := drainOutputs(ch)
	if n := countOutputs(outs, OutputSendToValidator, MsgVote); n != 0 {
		t.Fatalf("voted %d times for the conflicting proposal", n)
	}
	if n := countOutputs(outs, OutputExecuteBlock, 0); n != 0 {
		t.Fatal("conflicting proposal requested execution")
	}
	checkProposalEvidence(t, setup, outs, 1, a, b)

	// Seeing the second proposal again does not re-emit evidence for a new
	// pair, and the store dedupes the same pair.
	deliverProposal(t, e, eqProposal(e, setup, 1, b))
	evs := proposalEvidence(drainOutputs(ch))
	store := NewEquivocationStore(8)
	stored := 0
	for _, o := range append(proposalEvidence(outs), evs...) {
		if store.RecordOutput(o) {
			stored++
		}
	}
	if stored != 1 {
		t.Fatalf("want one stored evidence entry, got %d", stored)
	}
}

// Both proposals arrive before either block is imported: the conflicting one
// is refused and the vote still goes to the first.
func TestProposalEquivocationBeforeVote(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, ch := newTestEngine(t, setup, 0)
	a, b := types.Hash{0xAA}, types.Hash{0xBB}
	deliverProposal(t, e, eqProposal(e, setup, 1, a))
	deliverProposal(t, e, eqProposal(e, setup, 1, b))
	outs := drainOutputs(ch)
	checkProposalEvidence(t, setup, outs, 1, a, b)

	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: b}); err != nil {
		t.Fatal(err)
	}
	if n := countOutputs(drainOutputs(ch), OutputSendToValidator, MsgVote); n != 0 {
		t.Fatal("voted for the conflicting proposal")
	}
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: a}); err != nil {
		t.Fatal(err)
	}
	for _, o := range drainOutputs(ch) {
		if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgVote {
			if v := o.Message.Payload.(*Vote); v.BlockHash != a {
				t.Fatalf("vote for %x, want the first proposal", v.BlockHash[:2])
			}
		}
	}
}

// The second proposal arrives after the node has left the view.
func TestProposalEquivocationAfterViewAdvanced(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, ch := newTestEngine(t, setup, 0)
	a, b := types.Hash{0xAA}, types.Hash{0xBB}
	deliverProposal(t, e, eqProposal(e, setup, 1, a))
	e.mu.Lock()
	err := e.advanceToView(3)
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	drainOutputs(ch)

	// A forged stale proposal (wrong key) is not evidence.
	forged := eqProposal(e, setup, 1, b)
	forged.Signature = setup.keys[2].Sign(e.proposalSigningMessage(1, b)).Marshal()
	deliverProposal(t, e, forged)
	if ev := proposalEvidence(drainOutputs(ch)); len(ev) != 0 {
		t.Fatal("forged stale proposal recorded as evidence")
	}

	deliverProposal(t, e, eqProposal(e, setup, 1, b))
	outs := drainOutputs(ch)
	checkProposalEvidence(t, setup, outs, 1, a, b)
	if n := countOutputs(outs, OutputSendToValidator, MsgVote); n != 0 {
		t.Fatal("stale proposal produced a vote")
	}
}

func TestProposalEquivocationWindowBounded(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	for v := ViewNumber(1); v <= 3*proposalEquivocationWindow; v++ {
		p := unsignedTestProposal(v)
		e.observeProposal(p, setup.pubKeys[0], []byte{1})
	}
	if len(e.proposalSeen) != proposalEquivocationWindow || len(e.proposalSeenOrder) != proposalEquivocationWindow {
		t.Fatalf("window not bounded: %d/%d", len(e.proposalSeen), len(e.proposalSeenOrder))
	}
	if _, ok := e.proposalSeen[3*proposalEquivocationWindow]; !ok {
		t.Fatal("newest view evicted")
	}
}

func unsignedTestProposal(v ViewNumber) *Proposal {
	return &Proposal{View: v, BlockHash: types.Hash{byte(v)}, Signature: []byte{2}}
}

// The service counts proposal evidence in hotstuff_equivocations and
// n42_equivocations_total and stores it for n42_equivocations.
func TestServiceCountsProposalEquivocation(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, ch := newTestEngine(t, setup, 0)
	deliverProposal(t, e, eqProposal(e, setup, 1, types.Hash{0xAA}))
	deliverProposal(t, e, eqProposal(e, setup, 1, types.Hash{0xBB}))
	evs := proposalEvidence(drainOutputs(ch))
	if len(evs) != 1 {
		t.Fatalf("want one evidence output, got %d", len(evs))
	}
	s, _, _ := hsTNewOutputService(t)
	hs, n42 := metricEquivocations.Get(), metricN42Equivocations.Get()
	total := DefaultEquivocationStore.Report().Total
	s.handleOutput(evs[0], time.Time{})
	if metricEquivocations.Get() != hs+1 || metricN42Equivocations.Get() != n42+1 {
		t.Fatal("equivocation counters not incremented")
	}
	rep := DefaultEquivocationStore.Report()
	if rep.Total != total+1 || rep.Evidence[len(rep.Evidence)-1].Kind != EvidenceKindProposal {
		t.Fatalf("proposal evidence not in the RPC store: %+v", rep.Total)
	}
}

// Profile 1: the evidence carries the H2-v4 proposal signing message, and the
// conflicting checked block gets no vote while the first one does.
func TestProfile1ProposalEquivocation(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, ch := newP1Engine(t, setup, 0)
	parent := p1TestIdentity.GenesisHash
	a, b := types.Hash{0xA1}, types.Hash{0xB1}
	deliverProposal(t, e, p1Proposal(setup, p1TestIdentity, 1, a, GenesisQC(), 1))
	deliverProposal(t, e, p1Proposal(setup, p1TestIdentity, 1, b, GenesisQC(), 1))
	outs := drainOutputs(ch)
	checkProposalEvidence(t, setup, outs, 1, a, b)
	ev := proposalEvidence(outs)[0]
	if string(ev.Msg2) != string(H2V4ProposalSigningMessage(p1TestIdentity, 1, b, types.Hash{})) {
		t.Fatal("evidence does not carry the H2-v4 proposal signing message")
	}
	p1Event(t, e, EventBlockImported, parent, types.Hash{})
	for _, h := range []types.Hash{b, a} {
		p1Event(t, e, EventBlockImported, h, parent)
		p1Event(t, e, EventBlockChecked, h, parent)
	}
	for _, o := range drainOutputs(ch) {
		if o.Type == OutputSendToValidator && o.Message != nil && o.Message.Type == MsgVote {
			if v := o.Message.Payload.(*Vote); v.BlockHash != a {
				t.Fatal("profile 1 voted for the conflicting proposal")
			}
		}
	}
}
