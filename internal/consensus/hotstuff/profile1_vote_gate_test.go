// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

// p1TestIdentity is the chain-bound H2-v4 identity of the rule tests.
var p1TestIdentity = H2V4ChainIdentity{ChainID: 1144, GenesisHash: types.Hash{0x6f, 0x73}}

// newP1Engine is a profile-1 engine of validator idx on setup's set.
func newP1Engine(t *testing.T, setup *testSetup, idx int) (*ConsensusEngine, chan EngineOutput) {
	t.Helper()
	e, ch := newTestEngine(t, setup, idx)
	e.EnableH2V4(p1TestIdentity)
	e.SetProfile1(setup.vs)
	return e, ch
}

// p1QC aggregates the signatures of keys[i] for i in signers over msg into
// a certificate checked against vs.
func p1QC(t *testing.T, keys []common.SecretKey, vs *ValidatorSet, view ViewNumber, blockHash types.Hash, msg []byte, signers ...int) QuorumCertificate {
	t.Helper()
	vc := NewVoteCollector(view, blockHash, vs.Len())
	for _, i := range signers {
		if err := vc.AddVote(ValidatorIndex(i), keys[i].Sign(msg)); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := vc.BuildQCWithMessage(vs, msg)
	if err != nil {
		t.Fatal(err)
	}
	return *qc
}

// p1VoteQC is a PrepareQC of the genesis set (validators 0, 1, 2).
func p1VoteQC(t *testing.T, setup *testSetup, id H2V4ChainIdentity, view ViewNumber, blockHash types.Hash) QuorumCertificate {
	return p1QC(t, setup.keys, setup.vs, view, blockHash, H2V4VoteSigningMessage(id, view, blockHash), 0, 1, 2)
}

// p1CommitQC is a CommitQC of the genesis set (validators 0, 1, 2).
func p1CommitQC(t *testing.T, setup *testSetup, id H2V4ChainIdentity, view ViewNumber, blockHash types.Hash) QuorumCertificate {
	return p1QC(t, setup.keys, setup.vs, view, blockHash, H2V4CommitSigningMessage(id, view, blockHash, types.Hash{}), 0, 1, 2)
}

// p1Proposal is a proposal signed by proposer in the H2-v4 proposal domain.
func p1Proposal(setup *testSetup, id H2V4ChainIdentity, view ViewNumber, blockHash types.Hash, justify QuorumCertificate, proposer int) *Proposal {
	return &Proposal{
		View:      view,
		BlockHash: blockHash,
		JustifyQC: justify,
		Proposer:  ValidatorIndex(proposer),
		Signature: setup.keys[proposer].Sign(H2V4ProposalSigningMessage(id, view, blockHash, types.Hash{})).Marshal(),
	}
}

func p1Deliver(e *ConsensusEngine, msgType ConsensusMsgType, payload interface{}) error {
	return e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: msgType, Payload: payload}})
}

func p1Event(t *testing.T, e *ConsensusEngine, typ ConsensusEventType, hash, parent types.Hash) {
	t.Helper()
	if err := e.ProcessEvent(ConsensusEvent{Type: typ, Hash: hash, ParentHash: parent}); err != nil {
		t.Fatalf("event %d: %v", typ, err)
	}
}

func p1Votes(ch chan EngineOutput, mt ConsensusMsgType) int {
	return countOutputs(drainOutputs(ch), OutputSendToValidator, mt)
}

// Round 1 on profile 1 waits for the deferred proposal check: a known header
// (the S31 header vote) and the block's import are each not enough (A13,
// AM19, AM23). The same events vote at once on a two-phase legacy engine.
func TestProfile1PrepareVoteNeedsTheDeferredCheck(t *testing.T) {
	setup := newTestSetup(t, 4)
	// Justified by the genesis QC, so the parent is the genesis block.
	block, parent := types.Hash{0xB1}, p1TestIdentity.GenesisHash

	legacy, legacyCh := newTestEngine(t, setup, 0)
	legacy.SetTwoPhaseVote(true)
	if err := p1Deliver(legacy, MsgProposal, signedProposal(setup, 1, block, GenesisQC())); err != nil {
		t.Fatal(err)
	}
	p1Event(t, legacy, EventBlockHeaderKnown, block, parent)
	if n := p1Votes(legacyCh, MsgVote); n != 1 {
		t.Fatalf("legacy two-phase engine: %d header votes, want 1", n)
	}

	e, ch := newP1Engine(t, setup, 0)
	if err := p1Deliver(e, MsgProposal, p1Proposal(setup, p1TestIdentity, 1, block, GenesisQC(), 1)); err != nil {
		t.Fatal(err)
	}
	p1Event(t, e, EventBlockHeaderKnown, block, parent)
	if n := p1Votes(ch, MsgVote); n != 0 {
		t.Fatalf("profile 1 voted on the header alone: %d", n)
	}
	p1Event(t, e, EventBlockImported, parent, types.Hash{})
	p1Event(t, e, EventBlockImported, block, parent)
	if n := p1Votes(ch, MsgVote); n != 0 {
		t.Fatalf("profile 1 voted on an import without the check: %d", n)
	}
	p1Event(t, e, EventBlockChecked, block, parent)
	if n := p1Votes(ch, MsgVote); n != 1 {
		t.Fatalf("profile 1 after the check: %d votes, want 1", n)
	}
}

// A checked block justified by the genesis QC must have the genesis block as
// its parent; there is no fail-open for the zero-hash justify on profile 1.
func TestProfile1GenesisJustifyRequiresTheGenesisParent(t *testing.T) {
	setup := newTestSetup(t, 4)
	block, other := types.Hash{0xB5}, types.Hash{0xA5}
	e, ch := newP1Engine(t, setup, 0)
	if err := p1Deliver(e, MsgProposal, p1Proposal(setup, p1TestIdentity, 1, block, GenesisQC(), 1)); err != nil {
		t.Fatal(err)
	}
	p1Event(t, e, EventBlockImported, other, types.Hash{})
	p1Event(t, e, EventBlockChecked, block, other)
	if n := p1Votes(ch, MsgVote); n != 0 {
		t.Fatalf("voted for a genesis-justified block whose parent is not genesis: %d", n)
	}
	if e.extendsJustify(1, block) {
		t.Fatal("extendsJustify passed a genesis-justified block on a non-genesis parent")
	}
	// Unknown parent: refused, not failed open.
	if e.extendsJustify(1, types.Hash{0xB6}) {
		t.Fatal("extendsJustify failed open on an unknown parent of a genesis-justified block")
	}

	// The same block on the genesis parent is voted.
	block2 := types.Hash{0xB7}
	e2, ch2 := newP1Engine(t, setup, 0)
	if err := p1Deliver(e2, MsgProposal, p1Proposal(setup, p1TestIdentity, 1, block2, GenesisQC(), 1)); err != nil {
		t.Fatal(err)
	}
	p1Event(t, e2, EventBlockChecked, block2, p1TestIdentity.GenesisHash)
	if n := p1Votes(ch2, MsgVote); n != 1 {
		t.Fatalf("genesis-parent block: %d votes, want 1", n)
	}
}

// A block that fails the deferred check never receives a Round-1 vote, even
// once it and its parent are imported.
func TestProfile1RefusedBlockGetsNoPrepareVote(t *testing.T) {
	setup := newTestSetup(t, 4)
	block, parent := types.Hash{0xB2}, types.Hash{0xA2}
	e, ch := newP1Engine(t, setup, 0)
	if err := p1Deliver(e, MsgProposal, p1Proposal(setup, p1TestIdentity, 1, block, GenesisQC(), 1)); err != nil {
		t.Fatal(err)
	}
	p1Event(t, e, EventBlockHeaderKnown, block, parent)
	p1Event(t, e, EventBlockImported, parent, types.Hash{})
	p1Event(t, e, EventBlockImported, block, parent)
	if n := p1Votes(ch, MsgVote); n != 0 {
		t.Fatalf("a block without a passing check got %d prepare votes", n)
	}
}

// Round 1 also waits for the parent (the JustifyQC block) to be imported.
func TestProfile1PrepareVoteWaitsForTheParentImport(t *testing.T) {
	setup := newTestSetup(t, 4)
	block, parent := types.Hash{0xB3}, types.Hash{0xA3}
	e, ch := newP1Engine(t, setup, 0)
	e.mu.Lock()
	if err := e.advanceToView(2); err != nil {
		t.Fatal(err)
	}
	e.mu.Unlock()
	drainOutputs(ch)

	justify := p1VoteQC(t, setup, p1TestIdentity, 1, parent)
	if err := p1Deliver(e, MsgProposal, p1Proposal(setup, p1TestIdentity, 2, block, justify, 2)); err != nil {
		t.Fatal(err)
	}
	p1Event(t, e, EventBlockChecked, block, parent)
	if n := p1Votes(ch, MsgVote); n != 0 {
		t.Fatalf("voted before the parent imported: %d", n)
	}
	p1Event(t, e, EventBlockImported, parent, types.Hash{})
	if n := p1Votes(ch, MsgVote); n != 1 {
		t.Fatalf("after the parent imported: %d votes, want 1", n)
	}
}

// A checked block whose parent is not the JustifyQC block is not voted.
func TestProfile1PrepareVoteRefusesANonExtendingBlock(t *testing.T) {
	setup := newTestSetup(t, 4)
	block, parent, other := types.Hash{0xB4}, types.Hash{0xA4}, types.Hash{0xC4}
	e, ch := newP1Engine(t, setup, 0)
	e.mu.Lock()
	if err := e.advanceToView(2); err != nil {
		t.Fatal(err)
	}
	e.mu.Unlock()
	drainOutputs(ch)

	justify := p1VoteQC(t, setup, p1TestIdentity, 1, parent)
	if err := p1Deliver(e, MsgProposal, p1Proposal(setup, p1TestIdentity, 2, block, justify, 2)); err != nil {
		t.Fatal(err)
	}
	p1Event(t, e, EventBlockImported, parent, types.Hash{})
	p1Event(t, e, EventBlockImported, other, types.Hash{})
	p1Event(t, e, EventBlockChecked, block, other)
	if n := p1Votes(ch, MsgVote); n != 0 {
		t.Fatalf("voted for a block that does not extend its JustifyQC block: %d", n)
	}
}

// The CommitVote waits for this node's own execution of the block: a checked
// block on an imported parent (the legacy deferred attestation) is not
// enough on profile 1 (A13, AM19).
func TestProfile1CommitVoteWaitsForOwnImport(t *testing.T) {
	setup := newTestSetup(t, 4)
	block, parent := types.Hash{0xB5}, types.Hash{0xA5}
	e, ch := newP1Engine(t, setup, 0)

	pqc := &PrepareQCMsg{View: 1, BlockHash: block, QC: p1VoteQC(t, setup, p1TestIdentity, 1, block)}
	if err := p1Deliver(e, MsgPrepareQC, pqc); err != nil {
		t.Fatal(err)
	}
	p1Event(t, e, EventBlockChecked, block, parent)
	p1Event(t, e, EventBlockImported, parent, types.Hash{})
	if n := p1Votes(ch, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote before this node executed the block: %d", n)
	}
	p1Event(t, e, EventBlockImported, block, parent)
	if n := p1Votes(ch, MsgCommitVote); n != 1 {
		t.Fatalf("after the own import: %d commit votes, want 1", n)
	}
}

// Certificates from a set other than the genesis set are refused on every
// engine path that verifies one (E13, AM5): a PrepareQC, a proposal's
// JustifyQC and a Decide's CommitQC signed by four foreign keys (a same-size
// set the legacy resolution would have accepted as a fallback).
func TestProfile1RefusesCertificatesOfAForeignSet(t *testing.T) {
	setup := newTestSetup(t, 4)
	foreign := newTestSetup(t, 4)
	block := types.Hash{0xB6}

	e, ch := newP1Engine(t, setup, 0)
	e.mu.Lock()
	e.epochManager.historicalSets[7] = foreign.vs // a known same-size set
	e.mu.Unlock()

	vqc := p1QC(t, foreign.keys, foreign.vs, 1, block, H2V4VoteSigningMessage(p1TestIdentity, 1, block), 0, 1, 2)
	if err := p1Deliver(e, MsgPrepareQC, &PrepareQCMsg{View: 1, BlockHash: block, QC: vqc}); err == nil {
		t.Fatal("a PrepareQC of a foreign set was accepted")
	}
	p1Event(t, e, EventBlockImported, block, types.Hash{})
	if n := p1Votes(ch, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote on a foreign PrepareQC: %d", n)
	}

	cqc := p1QC(t, foreign.keys, foreign.vs, 1, block, H2V4CommitSigningMessage(p1TestIdentity, 1, block, types.Hash{}), 0, 1, 2)
	if err := p1Deliver(e, MsgDecide, &Decide{View: 1, BlockHash: block, CommitQC: cqc}); err == nil {
		t.Fatal("a Decide with a foreign CommitQC was accepted")
	}
	if got := e.LastCommittedQC(); got.View != 0 {
		t.Fatalf("committed view %d on a foreign CommitQC", got.View)
	}

	e.mu.Lock()
	if err := e.advanceToView(2); err != nil {
		t.Fatal(err)
	}
	e.mu.Unlock()
	if err := p1Deliver(e, MsgProposal, p1Proposal(setup, p1TestIdentity, 2, types.Hash{0xB7}, vqc, 2)); err == nil {
		t.Fatal("a proposal justified by a foreign QC was accepted")
	}
}

// Anchors are commit-domain only: a Decide whose certificate is a PrepareQC
// (vote domain) of the genesis set does not commit (A14, E13).
func TestProfile1RefusesAPrepareDomainDecide(t *testing.T) {
	setup := newTestSetup(t, 4)
	block := types.Hash{0xB8}
	e, _ := newP1Engine(t, setup, 0)
	qc := p1VoteQC(t, setup, p1TestIdentity, 1, block)
	if err := p1Deliver(e, MsgDecide, &Decide{View: 1, BlockHash: block, CommitQC: qc}); err == nil {
		t.Fatal("a vote-domain certificate committed a block")
	}
	if got := e.LastCommittedQC(); got.View != 0 {
		t.Fatalf("committed view %d on a vote-domain certificate", got.View)
	}
	ok := p1CommitQC(t, setup, p1TestIdentity, 1, block)
	if err := p1Deliver(e, MsgDecide, &Decide{View: 1, BlockHash: block, CommitQC: ok}); err != nil {
		t.Fatalf("a genuine CommitQC: %v", err)
	}
}

// Legacy (pre-H2-v4) signing domains are refused (A14).
func TestProfile1RefusesALegacyDomainQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	block := types.Hash{0xB9}
	e, _ := newP1Engine(t, setup, 0)
	qc := p1QC(t, setup.keys, setup.vs, 1, block, SigningMessage(1, block), 0, 1, 2)
	if err := p1Deliver(e, MsgPrepareQC, &PrepareQCMsg{View: 1, BlockHash: block, QC: qc}); err == nil {
		t.Fatal("a legacy-domain PrepareQC was accepted")
	}
}

// A certificate whose bitmap is not the genesis set's width fails closed
// with ErrProfile1CertSet; an engine without the H2-v4 identity fails closed
// with ErrProfile1Domain.
func TestProfile1CertificateFailsClosed(t *testing.T) {
	setup := newTestSetup(t, 4)
	wide := newTestSetup(t, 5)
	block := types.Hash{0xBA}
	e, _ := newP1Engine(t, setup, 0)
	qc := p1QC(t, wide.keys, wide.vs, 1, block, H2V4VoteSigningMessage(p1TestIdentity, 1, block), 0, 1, 2, 3)
	if err := p1Deliver(e, MsgPrepareQC, &PrepareQCMsg{View: 1, BlockHash: block, QC: qc}); !errors.Is(err, ErrProfile1CertSet) {
		t.Fatalf("a 5-wide certificate: %v, want ErrProfile1CertSet", err)
	}

	bare, _ := newTestEngine(t, setup, 0)
	bare.SetProfile1(setup.vs)
	good := p1VoteQC(t, setup, p1TestIdentity, 1, block)
	if err := p1Deliver(bare, MsgPrepareQC, &PrepareQCMsg{View: 1, BlockHash: block, QC: good}); !errors.Is(err, ErrProfile1Domain) {
		t.Fatalf("no H2-v4 identity: %v, want ErrProfile1Domain", err)
	}
}

// Profile 1 resolves every certificate to the genesis set, whatever its width.
func TestProfile1ResolvesOnlyTheGenesisSet(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	if got := e.resolveQCValidatorSet(1, 4); got != setup.vs {
		t.Fatal("the view's set was not chosen")
	}
	p1, _ := newP1Engine(t, setup, 0)
	if got := p1.resolveQCValidatorSet(1, 7); got != setup.vs {
		t.Fatal("profile 1 resolved a set other than the genesis set")
	}
}
